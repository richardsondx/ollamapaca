//go:build windows || darwin

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOAuthMCP is an MCP server protected by OAuth, with its own
// authorization server: metadata, registration, authorize (which
// redirects straight back, like a user who clicked Allow), token and
// refresh.
type fakeOAuthMCP struct {
	mu           sync.Mutex
	srv          *httptest.Server
	codes        map[string]string // code -> PKCE challenge
	valid        map[string]bool   // access tokens
	refreshes    int
	expiresIn    int
	registered   []string
	lastResource string
}

func newFakeOAuthMCP(t *testing.T) *fakeOAuthMCP {
	f := &fakeOAuthMCP{codes: map[string]string{}, valid: map[string]bool{}, expiresIn: 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"resource": f.srv.URL + "/mcp", "authorization_servers": []string{f.srv.URL + "/auth"}, "scopes_supported": []string{"tools"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL + "/auth", "authorization_endpoint": f.srv.URL + "/auth/authorize",
			"token_endpoint": f.srv.URL + "/auth/token", "registration_endpoint": f.srv.URL + "/auth/register",
		})
	})
	mux.HandleFunc("/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registered = append(f.registered, body.RedirectURIs...)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"client_id": "client-1"})
	})
	mux.HandleFunc("/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "client-1" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "tools" {
			http.Error(w, "bad authorize request "+r.URL.RawQuery, 400)
			return
		}
		code := "code-" + randomString(4)
		f.mu.Lock()
		f.codes[code] = q.Get("code_challenge")
		f.lastResource = q.Get("resource")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			challenge, ok := f.codes[r.Form.Get("code")]
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			delete(f.codes, r.Form.Get("code"))
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			f.refreshes++
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, 400)
			return
		}
		tok := "access-" + randomString(4)
		f.valid[tok] = true
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "refresh_token": "refresh-1", "token_type": "Bearer", "expires_in": f.expiresIn})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.valid[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if out := fakeMCPHandle(body); out != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(out)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// followLikeABrowser opens the authorize URL and follows the redirect
// back to the loopback callback, as the user's browser would.
func followLikeABrowser(t *testing.T, authURL string) {
	t.Helper()
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Signed in") {
		t.Fatalf("callback page: %d %s", resp.StatusCode, body)
	}
}

func TestMCPOAuthSignInAndRefresh(t *testing.T) {
	t.Setenv("OLLAMAPACA_HOME", t.TempDir())
	oauthStoreOnce = sync.Once{}
	f := newFakeOAuthMCP(t)
	serverURL := f.srv.URL + "/mcp"

	m := NewMCPManager(nil)
	defer m.Close()
	m.Sync([]MCPServerSpec{{Key: "fake", Name: "fake", Config: MCPServerConfig{URL: serverURL}}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Before sign-in: the server answers 401, the status says so.
	if _, err := m.Tools(ctx, "fake"); !errors.Is(err, ErrNeedsSignIn) {
		t.Fatalf("want ErrNeedsSignIn, got %v", err)
	}
	if st := m.Status()[0]; st.State != "needs_auth" || st.SignedIn {
		t.Fatalf("status before sign-in: %+v", st)
	}

	authURL, err := m.SignIn(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	followLikeABrowser(t, authURL)
	if f.lastResource != serverURL {
		t.Fatalf("resource indicator = %q", f.lastResource)
	}
	// The manager reconnects in the background after sign-in.
	deadline := time.Now().Add(5 * time.Second)
	for m.Status()[0].State != "ready" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if st := m.Status()[0]; st.State != "ready" || !st.SignedIn || len(st.Tools) != 2 {
		t.Fatalf("status after sign-in: %+v", st)
	}
	res, err := m.Call(ctx, "fake", "echo", map[string]any{"text": "hi"})
	if err != nil || res.Text != "echo: hi" {
		t.Fatalf("call: %+v %v", res, err)
	}

	// The server revokes every token: the client refreshes once and
	// the call still works.
	f.mu.Lock()
	f.valid = map[string]bool{}
	f.mu.Unlock()
	res, err = m.Call(ctx, "fake", "echo", map[string]any{"text": "again"})
	if err != nil || res.Text != "echo: again" || f.refreshes != 1 {
		t.Fatalf("call after revoke: %+v %v refreshes=%d", res, err, f.refreshes)
	}

	// A second sign-in reuses the registered client.
	if err := m.SignOut("fake"); err != nil {
		t.Fatal(err)
	}
	authURL, err = m.SignIn(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	followLikeABrowser(t, authURL)
	if len(f.registered) != 1 {
		t.Fatalf("client registered %d times, want 1", len(f.registered))
	}
}

func TestMCPOAuthExplicitHeaderSkipsSignIn(t *testing.T) {
	t.Setenv("OLLAMAPACA_HOME", t.TempDir())
	oauthStoreOnce = sync.Once{}
	f := newFakeOAuthMCP(t)
	f.valid["my-api-key"] = true
	checkFakeServer(t, MCPServerConfig{URL: f.srv.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer my-api-key"}})
}

func TestResourceMetadataFromChallenge(t *testing.T) {
	for in, want := range map[string]string{
		`Bearer error="invalid_token", resource_metadata="https://x/.well-known/oauth-protected-resource"`: "https://x/.well-known/oauth-protected-resource",
		`Bearer resource_metadata=https://y/meta, scope="a"`:                                               "https://y/meta",
		`Bearer realm="x"`: "",
	} {
		if got := resourceMetadataFromChallenge(in); got != want {
			t.Fatalf("%s: got %q want %q", in, got, want)
		}
	}
	_ = fmt.Sprint
}
