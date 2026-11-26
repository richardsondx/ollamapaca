//go:build windows || darwin

package tools

// OAuth sign-in for remote MCP servers, following the MCP
// authorization spec: protected resource metadata (RFC 9728) points
// at an authorization server (RFC 8414 metadata), the client
// registers itself when it has no client id (RFC 7591), and signs in
// with the authorization code flow, PKCE S256 and a loopback
// redirect. Tokens are stored in ~/.ollamapaca/oauth.json (0600) and
// refreshed when they expire.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// MCPOAuthConfig is an OAuth client for one server. Empty ClientID
// means dynamic client registration.
type MCPOAuthConfig struct {
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// ErrNeedsSignIn means the server asked for OAuth and there is no
// usable token. The Plugins page shows a Connect button for it.
var ErrNeedsSignIn = errors.New("sign-in required: click Connect on the Plugins page")

type oauthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

type oauthRecord struct {
	Resource              string      `json:"resource,omitempty"`
	Issuer                string      `json:"issuer,omitempty"`
	AuthorizationEndpoint string      `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string      `json:"token_endpoint,omitempty"`
	ClientID              string      `json:"client_id,omitempty"`
	ClientSecret          string      `json:"client_secret,omitempty"`
	RedirectURI           string      `json:"redirect_uri,omitempty"`
	Registered            bool        `json:"registered,omitempty"`
	Token                 *oauthToken `json:"token,omitempty"`
}

// oauthStoreFile keeps records keyed by MCP server URL.
type oauthStoreFile struct {
	mu   sync.Mutex
	path string
}

var (
	oauthStoreOnce sync.Once
	oauthStoreInst *oauthStoreFile
)

func oauthStore() *oauthStoreFile {
	oauthStoreOnce.Do(func() {
		dir := os.Getenv("OLLAMAPACA_HOME")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".ollamapaca")
		}
		oauthStoreInst = &oauthStoreFile{path: filepath.Join(dir, "oauth.json")}
	})
	return oauthStoreInst
}

func (s *oauthStoreFile) load() map[string]*oauthRecord {
	out := map[string]*oauthRecord{}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func (s *oauthStoreFile) get(key string) *oauthRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.load()[key]; ok {
		return r
	}
	return nil
}

func (s *oauthStoreFile) put(key string, r *oauthRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.load()
	if r == nil {
		delete(all, key)
	} else {
		all[key] = r
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// MCPSignedIn reports whether a token is stored for a server.
func MCPSignedIn(serverURL string) bool {
	r := oauthStore().get(serverURL)
	return r != nil && r.Token != nil && r.Token.AccessToken != ""
}

// MCPSignOut forgets the token for a server, keeping the client
// registration so the next sign-in is quicker.
func MCPSignOut(serverURL string) error {
	r := oauthStore().get(serverURL)
	if r == nil {
		return nil
	}
	r.Token = nil
	return oauthStore().put(serverURL, r)
}

var oauthHTTP = &http.Client{Timeout: 20 * time.Second}

// accessToken returns a valid access token for a server, refreshing
// it if it expired. It returns "" when there is none.
func accessToken(ctx context.Context, serverURL string) (string, error) {
	r := oauthStore().get(serverURL)
	if r == nil || r.Token == nil || r.Token.AccessToken == "" {
		return "", nil
	}
	if !r.Token.Expiry.IsZero() && time.Until(r.Token.Expiry) < time.Minute {
		if err := refreshToken(ctx, serverURL, r); err != nil {
			return "", err
		}
	}
	return r.Token.AccessToken, nil
}

func refreshToken(ctx context.Context, serverURL string, r *oauthRecord) error {
	if r.Token == nil || r.Token.RefreshToken == "" || r.TokenEndpoint == "" {
		return ErrNeedsSignIn
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {r.Token.RefreshToken},
		"client_id":     {r.ClientID},
	}
	if r.ClientSecret != "" {
		form.Set("client_secret", r.ClientSecret)
	}
	if r.Resource != "" {
		form.Set("resource", r.Resource)
	}
	tok, err := tokenRequest(ctx, r.TokenEndpoint, form)
	if err != nil {
		// A refresh token that no longer works means signing in again.
		r.Token = nil
		_ = oauthStore().put(serverURL, r)
		return fmt.Errorf("%w (%v)", ErrNeedsSignIn, err)
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = r.Token.RefreshToken
	}
	r.Token = tok
	return oauthStore().put(serverURL, r)
}

// forceRefresh is used after a 401 with a token that looked valid.
func forceRefresh(ctx context.Context, serverURL string) (string, error) {
	r := oauthStore().get(serverURL)
	if r == nil || r.Token == nil {
		return "", ErrNeedsSignIn
	}
	if err := refreshToken(ctx, serverURL, r); err != nil {
		return "", err
	}
	return r.Token.AccessToken, nil
}

func tokenRequest(ctx context.Context, endpoint string, form url.Values) (*oauthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		TokenType    string          `json:"token_type"`
		ExpiresIn    json.Number     `json:"expires_in"`
		Error        string          `json:"error"`
		ErrorDesc    string          `json:"error_description"`
		Extra        json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		// Some servers answer form-encoded.
		v, perr := url.ParseQuery(string(body))
		if perr != nil {
			return nil, fmt.Errorf("token endpoint answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		out.AccessToken, out.RefreshToken, out.TokenType = v.Get("access_token"), v.Get("refresh_token"), v.Get("token_type")
		out.ExpiresIn = json.Number(v.Get("expires_in"))
		out.Error, out.ErrorDesc = v.Get("error"), v.Get("error_description")
	}
	if out.Error != "" || out.AccessToken == "" {
		msg := out.Error
		if out.ErrorDesc != "" {
			msg += ": " + out.ErrorDesc
		}
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("token request failed: %s", msg)
	}
	tok := &oauthToken{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, TokenType: out.TokenType}
	if n, err := out.ExpiresIn.Int64(); err == nil && n > 0 {
		tok.Expiry = time.Now().Add(time.Duration(n) * time.Second)
	}
	return tok, nil
}

func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

type protectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type authServerMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

func wellKnown(base *url.URL, name string) []string {
	origin := base.Scheme + "://" + base.Host
	path := strings.TrimRight(base.Path, "/")
	var out []string
	if path != "" {
		out = append(out, origin+"/.well-known/"+name+path)
	}
	return append(out, origin+"/.well-known/"+name)
}

// discoverResource finds the protected resource metadata for a server.
func discoverResource(ctx context.Context, serverURL string) (*protectedResource, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	candidates := wellKnown(&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}, "oauth-protected-resource")
	// The 401 challenge may name the metadata URL directly.
	if req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"ping"}`)); err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if resp, err := oauthHTTP.Do(req); err == nil {
			resp.Body.Close()
			if m := resourceMetadataFromChallenge(resp.Header.Get("WWW-Authenticate")); m != "" {
				candidates = append([]string{m}, candidates...)
			}
		}
	}
	for _, c := range candidates {
		var pr protectedResource
		if err := getJSON(ctx, c, &pr); err == nil && len(pr.AuthorizationServers) > 0 {
			return &pr, nil
		}
	}
	return nil, errors.New("no OAuth protected resource metadata")
}

func resourceMetadataFromChallenge(h string) string {
	i := strings.Index(h, "resource_metadata=")
	if i < 0 {
		return ""
	}
	v := h[i+len("resource_metadata="):]
	if strings.HasPrefix(v, `"`) {
		v = v[1:]
		if j := strings.Index(v, `"`); j >= 0 {
			return v[:j]
		}
	}
	if j := strings.IndexAny(v, ", "); j >= 0 {
		v = v[:j]
	}
	return v
}

func discoverAuthServer(ctx context.Context, issuer string) (*authServerMeta, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	candidates := append(wellKnown(u, "oauth-authorization-server"), wellKnown(u, "openid-configuration")...)
	candidates = append(candidates, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration")
	for _, c := range candidates {
		var m authServerMeta
		if err := getJSON(ctx, c, &m); err == nil && m.AuthorizationEndpoint != "" && m.TokenEndpoint != "" {
			return &m, nil
		}
	}
	return nil, fmt.Errorf("no OAuth authorization server metadata at %s", issuer)
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// MCPOAuthLogin is a sign-in in progress.
type MCPOAuthLogin struct {
	AuthURL string
	done    chan error
}

// Wait blocks until the browser sign-in finishes or ctx ends.
func (l *MCPOAuthLogin) Wait(ctx context.Context) error {
	select {
	case err := <-l.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartMCPOAuthLogin prepares a browser sign-in for an MCP server and
// starts the loopback listener that receives the result. Open
// AuthURL in the user's browser, then Wait.
func StartMCPOAuthLogin(ctx context.Context, serverURL string, cfg *MCPOAuthConfig) (*MCPOAuthLogin, error) {
	if cfg == nil {
		cfg = &MCPOAuthConfig{}
	}
	pr, err := discoverResource(ctx, serverURL)
	issuer := ""
	var scopes []string
	resource := serverURL
	if err == nil {
		issuer = pr.AuthorizationServers[0]
		scopes = pr.ScopesSupported
		if pr.Resource != "" {
			resource = pr.Resource
		}
	} else {
		u, _ := url.Parse(serverURL)
		issuer = u.Scheme + "://" + u.Host
	}
	if len(cfg.Scopes) > 0 {
		scopes = cfg.Scopes
	}
	meta, err := discoverAuthServer(ctx, issuer)
	if err != nil {
		return nil, err
	}

	rec := oauthStore().get(serverURL)
	if rec == nil {
		rec = &oauthRecord{}
	}
	// Reuse the registered redirect port when we can.
	var ln net.Listener
	if rec.RedirectURI != "" && cfg.ClientID == "" {
		if u, err := url.Parse(rec.RedirectURI); err == nil {
			ln, _ = net.Listen("tcp", u.Host)
		}
	}
	if ln == nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
	}
	redirect := fmt.Sprintf("http://%s/callback", ln.Addr().String())

	clientID, clientSecret := cfg.ClientID, cfg.ClientSecret
	if clientID == "" {
		if rec.Registered && rec.ClientID != "" && rec.RedirectURI == redirect && rec.Issuer == meta.Issuer {
			clientID, clientSecret = rec.ClientID, rec.ClientSecret
		} else {
			if meta.RegistrationEndpoint == "" {
				ln.Close()
				return nil, errors.New("this server does not let apps register themselves; add an OAuth client ID in the plugin settings, or use an access token")
			}
			id, secret, err := registerClient(ctx, meta.RegistrationEndpoint, redirect)
			if err != nil {
				ln.Close()
				return nil, err
			}
			clientID, clientSecret = id, secret
			rec.Registered = true
		}
	} else {
		rec.Registered = false
	}
	rec.Resource = resource
	rec.Issuer = meta.Issuer
	rec.AuthorizationEndpoint = meta.AuthorizationEndpoint
	rec.TokenEndpoint = meta.TokenEndpoint
	rec.ClientID = clientID
	rec.ClientSecret = clientSecret
	rec.RedirectURI = redirect
	if err := oauthStore().put(serverURL, rec); err != nil {
		ln.Close()
		return nil, err
	}

	verifier := randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomString(24)
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	if strings.Contains(meta.Issuer, "accounts.google.com") {
		// Google only returns a refresh token for offline access.
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
	}
	authURL := meta.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}

	login := &MCPOAuthLogin{AuthURL: authURL, done: make(chan error, 1)}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	var once sync.Once
	finish := func(err error) {
		once.Do(func() {
			login.done <- err
			// Shutdown stops listening at once (so the port can be
			// reused) and waits for the callback page to be written.
			go func() {
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(sctx)
			}()
		})
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		qs := r.URL.Query()
		if qs.Get("state") != state {
			writeCallbackPage(w, false, "The sign-in response did not match. Try Connect again.")
			finish(errors.New("state mismatch"))
			return
		}
		if e := qs.Get("error"); e != "" {
			msg := e
			if d := qs.Get("error_description"); d != "" {
				msg += ": " + d
			}
			writeCallbackPage(w, false, msg)
			finish(fmt.Errorf("sign-in was not completed: %s", msg))
			return
		}
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {qs.Get("code")},
			"redirect_uri":  {redirect},
			"client_id":     {clientID},
			"code_verifier": {verifier},
			"resource":      {resource},
		}
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
		tok, err := tokenRequest(r.Context(), meta.TokenEndpoint, form)
		if err != nil {
			writeCallbackPage(w, false, err.Error())
			finish(err)
			return
		}
		rec := oauthStore().get(serverURL)
		if rec == nil {
			rec = &oauthRecord{}
		}
		rec.Token = tok
		if err := oauthStore().put(serverURL, rec); err != nil {
			writeCallbackPage(w, false, err.Error())
			finish(err)
			return
		}
		writeCallbackPage(w, true, "")
		finish(nil)
	})
	go func() { _ = srv.Serve(ln) }()
	go func() {
		time.Sleep(10 * time.Minute)
		finish(errors.New("sign-in timed out"))
	}()
	return login, nil
}

func registerClient(ctx context.Context, endpoint, redirect string) (string, string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "Ollamapaca",
		"client_uri":                 "https://github.com/richardsondx/ollamapaca",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if resp.StatusCode >= 300 || json.Unmarshal(raw, &out) != nil || out.ClientID == "" {
		return "", "", fmt.Errorf("app registration failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return out.ClientID, out.ClientSecret, nil
}

func writeCallbackPage(w http.ResponseWriter, ok bool, msg string) {
	title, text := "Signed in", "You can close this tab and go back to Ollamapaca."
	if !ok {
		title, text = "Sign-in failed", msg
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title><body style="font-family:system-ui;max-width:32rem;margin:15vh auto;text-align:center"><h2>%s</h2><p>%s</p></body>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
}

// OpenInBrowser opens a URL in the user's default browser.
func OpenInBrowser(u string) error {
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("refusing to open %q", u)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}
