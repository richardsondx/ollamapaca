//go:build windows || darwin

package ui

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/app/store"
)

// TestPluginChatEndToEnd runs the real chat handler against a real
// Ollama model, with the browser plugin (through the plugin executor
// and the Chrome extension) and a user MCP server. It is opt in:
//
//	OLLAMAPACA_E2E_MODEL=qwen2.5:7b-instruct \
//	OLLAMAPACA_E2E_PROMPT='@browser open http://127.0.0.1:8765 and click Say hello' \
//	go test ./app/ui -run TestPluginChatEndToEnd -v
//
// OLLAMAPACA_E2E_MCP may hold a JSON {"mcpServers": {...}} to add.
func TestPluginChatEndToEnd(t *testing.T) {
	model := os.Getenv("OLLAMAPACA_E2E_MODEL")
	if model == "" {
		t.Skip("set OLLAMAPACA_E2E_MODEL to run")
	}
	home := t.TempDir()
	t.Setenv("OLLAMAPACA_HOME", home)
	if mcp := os.Getenv("OLLAMAPACA_E2E_MCP"); mcp != "" {
		if err := os.WriteFile(filepath.Join(home, "mcp.json"), []byte(mcp), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Store: &store.Store{DBPath: filepath.Join(home, "db.sqlite")}, Dev: true}
	defer s.Shutdown()
	for _, p := range strings.Split(os.Getenv("OLLAMAPACA_E2E_PLUGINS"), ",") {
		if p != "" && !s.plugins().reg.Enable(p) {
			t.Fatalf("cannot enable plugin %q, errors: %v", p, s.plugins().reg.Errors())
		}
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	prompt := os.Getenv("OLLAMAPACA_E2E_PROMPT")
	body, _ := json.Marshal(map[string]any{"model": model, "prompt": prompt})
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Post(srv.URL+"/api/v1/chat/new", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var answer strings.Builder
	tools := 0
	for sc.Scan() {
		if os.Getenv("OLLAMAPACA_E2E_RAW") != "" {
			t.Logf("RAW %s", sc.Text())
		}
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev["eventName"] {
		case "chat":
			if c, ok := ev["content"].(string); ok {
				answer.WriteString(c)
			}
		case "tool_call":
			b, _ := json.Marshal(ev["toolCalls"])
			t.Logf("TOOL CALL %s", b)
		case "tool_result":
			tools++
			c, _ := ev["content"].(string)
			if len(c) > 300 {
				c = c[:300] + "..."
			}
			t.Logf("TOOL RESULT %v: %s", ev["toolName"], c)
		case "error":
			t.Logf("ERROR EVENT %v", ev)
		}
	}
	t.Logf("ANSWER: %s", answer.String())
	if tools == 0 {
		t.Fatalf("the model called no tools")
	}
}

// TestServeUIForManualCheck serves the desktop UI and API on
// OLLAMAPACA_SERVE_ADDR until killed, for checking the Plugins page
// in a normal browser. Opt in only.
func TestServeUIForManualCheck(t *testing.T) {
	addr := os.Getenv("OLLAMAPACA_SERVE_ADDR")
	if addr == "" {
		t.Skip("set OLLAMAPACA_SERVE_ADDR to run")
	}
	home := os.Getenv("OLLAMAPACA_HOME")
	if home == "" {
		home = t.TempDir()
		t.Setenv("OLLAMAPACA_HOME", home)
	}
	s := &Server{Store: &store.Store{DBPath: filepath.Join(home, "db.sqlite")}, Dev: true}
	defer s.Shutdown()
	t.Fatal(http.ListenAndServe(addr, s.Handler()))
}

// TestLivePresets enables every bundled plugin in a temp home and
// prints what each MCP server does without credentials. Opt in.
func TestLivePresets(t *testing.T) {
	if os.Getenv("OLLAMAPACA_LIVE_PRESETS") == "" {
		t.Skip("set OLLAMAPACA_LIVE_PRESETS=1 to run")
	}
	t.Setenv("OLLAMAPACA_HOME", t.TempDir())
	svc := newPluginService(nil)
	defer svc.mcp.Close()
	for _, p := range svc.reg.List() {
		if len(p.MCPServers) > 0 {
			svc.reg.Enable(p.Name)
		}
	}
	svc.syncMCP()
	svc.warmUp()
	for _, st := range svc.mcp.Status() {
		names := []string{}
		for _, tl := range st.Tools {
			names = append(names, tl.Name)
		}
		errText := st.Error
		if len(errText) > 160 {
			errText = errText[:160]
		}
		t.Logf("%-28s %-12s tools=%d %v %s", st.Key, st.State, len(st.Tools), names, errText)
	}
}
