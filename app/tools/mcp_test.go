//go:build windows || darwin

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMCPHandle answers one JSON-RPC request like a tiny MCP server
// with an "echo" tool and a "fail" tool. It returns nil for
// notifications.
func fakeMCPHandle(raw []byte) []byte {
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil || msg.ID == nil {
		return nil
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
	switch msg.Method {
	case "initialize":
		reply["result"] = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake", "version": "1"},
		}
	case "tools/list":
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.Cursor == "" {
			reply["result"] = map[string]any{
				"tools": []any{map[string]any{
					"name": "echo", "description": "Echo text back.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
				}},
				"nextCursor": "page2",
			}
		} else {
			reply["result"] = map[string]any{
				"tools": []any{map[string]any{"name": "fail", "description": "Always fails."}},
			}
		}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.Name == "fail" {
			reply["result"] = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "boom"}}}
		} else {
			reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprint("echo: ", p.Arguments["text"])}}}
		}
	default:
		reply["error"] = map[string]any{"code": -32601, "message": "no such method"}
	}
	b, _ := json.Marshal(reply)
	return b
}

// TestHelperMCPServer is not a real test: it is the stdio MCP server
// started by TestMCPStdio, using the test binary itself.
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv("OLLAMAPACA_FAKE_MCP") != "1" {
		t.Skip("helper process")
	}
	fmt.Fprintln(os.Stderr, "fake server starting")
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if out := fakeMCPHandle(sc.Bytes()); out != nil {
			os.Stdout.Write(append(out, '\n'))
		}
	}
	os.Exit(0)
}

func checkFakeServer(t *testing.T, cfg MCPServerConfig) {
	t.Helper()
	m := NewMCPManager(nil)
	defer m.Close()
	m.Sync([]MCPServerSpec{{Key: "fake", Name: "fake", Config: cfg}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	adapters, err := m.AdaptersFor(ctx, "fake")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(adapters) != 2 {
		t.Fatalf("want 2 tools across 2 pages, got %d", len(adapters))
	}
	if adapters[0].Name() != "mcp__fake__echo" {
		t.Fatalf("unexpected tool name %q", adapters[0].Name())
	}
	_, content, err := adapters[0].Execute(ctx, map[string]any{"text": "hi"})
	if err != nil || content != "echo: hi" {
		t.Fatalf("echo: content=%q err=%v", content, err)
	}
	if _, _, err := adapters[1].Execute(ctx, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("isError must be an error, got %v", err)
	}
	st := m.Status()
	if len(st) != 1 || st[0].State != "ready" || len(st[0].Tools) != 2 {
		t.Fatalf("unexpected status %+v", st)
	}
}

func TestMCPStdio(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkFakeServer(t, MCPServerConfig{
		Command: exe,
		Args:    []string{"-test.run=^TestHelperMCPServer$"},
		Env:     map[string]string{"OLLAMAPACA_FAKE_MCP": "1"},
	})
}

func TestMCPStdioRestartsAfterCrash(t *testing.T) {
	exe, _ := os.Executable()
	m := NewMCPManager(nil)
	defer m.Close()
	m.Sync([]MCPServerSpec{{Key: "fake", Name: "fake", Config: MCPServerConfig{
		Command: exe, Args: []string{"-test.run=^TestHelperMCPServer$"}, Env: map[string]string{"OLLAMAPACA_FAKE_MCP": "1"},
	}}})
	ctx := context.Background()
	if _, err := m.Tools(ctx, "fake"); err != nil {
		t.Fatal(err)
	}
	st, _ := m.get("fake")
	st.client.t.(*stdioTransport).cmd.Process.Kill()
	<-st.client.t.(*stdioTransport).done
	res, err := m.Call(ctx, "fake", "echo", map[string]any{"text": "again"})
	if err != nil || res.Text != "echo: again" {
		t.Fatalf("call after crash: %+v %v", res, err)
	}
}

func TestMCPStreamableHTTP(t *testing.T) {
	for _, mode := range []string{"json", "sse"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			sessions := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				body, _ := io.ReadAll(r.Body)
				var msg rpcMessage
				_ = json.Unmarshal(body, &msg)
				if msg.Method == "initialize" {
					mu.Lock()
					sessions++
					mu.Unlock()
					w.Header().Set("Mcp-Session-Id", "s1")
				} else if r.Header.Get("Mcp-Session-Id") != "s1" {
					http.Error(w, "missing session", http.StatusBadRequest)
					return
				}
				out := fakeMCPHandle(body)
				if out == nil {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if mode == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, ": comment\n\nevent: message\ndata: %s\n\n", out)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(out)
			}))
			defer srv.Close()
			checkFakeServer(t, MCPServerConfig{URL: srv.URL + "/mcp"})
		})
	}
}

func TestMCPLegacySSE(t *testing.T) {
	type conn struct{ ch chan []byte }
	var mu sync.Mutex
	var current *conn
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Streamable HTTP probe: old servers refuse POST here.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		c := &conn{ch: make(chan []byte, 16)}
		mu.Lock()
		current = c
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: endpoint\ndata: /messages?session=1\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case b := <-c.ch:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		c := current
		mu.Unlock()
		if out := fakeMCPHandle(body); out != nil {
			c.ch <- out
		}
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// Explicit sse type.
	checkFakeServer(t, MCPServerConfig{Type: "sse", URL: srv.URL + "/sse"})
	// Untyped URL: streamable HTTP is tried first and falls back.
	checkFakeServer(t, MCPServerConfig{URL: srv.URL + "/sse"})
}

func TestMCPConfigRoundTrip(t *testing.T) {
	path := t.TempDir() + "/mcp.json"
	cfg, err := LoadMCPConfig(path)
	if err != nil || len(cfg.MCPServers) != 0 {
		t.Fatalf("missing file should be empty config: %v", err)
	}
	cfg.MCPServers["fs"] = MCPServerConfig{Command: "npx", Args: []string{"-y", "server"}}
	if err := SaveMCPConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	again, err := LoadMCPConfig(path)
	if err != nil || again.MCPServers["fs"].Command != "npx" || again.MCPServers["fs"].Transport() != "stdio" {
		t.Fatalf("round trip failed: %+v %v", again, err)
	}
}
