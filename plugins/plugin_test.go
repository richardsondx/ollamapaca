package plugins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlugin(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name": "` + name + `", "description": "Test plugin ` + name + `.", "version": "0.1.0", "tools": [{"name": "do_thing", "description": "Do a thing.", "parameters": {"type": "object", "properties": {}}}]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: " + name + "\ndescription: Test plugin " + name + ".\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverAndEnable(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "browser")
	writePlugin(t, root, "computer")
	reg := NewRegistry(root)
	if len(reg.List()) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(reg.List()))
	}
	if len(reg.Errors()) != 0 {
		t.Fatalf("expected no errors, got %v", reg.Errors())
	}
	if len(reg.EnabledTools()) != 0 {
		t.Fatalf("disabled plugins must expose no tools")
	}
	if !reg.Enable("browser") {
		t.Fatalf("enable should succeed for a known plugin")
	}
	if reg.Enable("nope") {
		t.Fatalf("enable should fail for an unknown plugin")
	}
	tools := reg.EnabledTools()
	if len(tools) != 1 || tools[0].Function.Name != "browser.do_thing" {
		t.Fatalf("unexpected enabled tools: %+v", tools)
	}
	if _, _, ok := reg.FindTool("computer", "do_thing"); ok {
		t.Fatalf("disabled plugin tool must not be found")
	}
	if _, _, ok := reg.FindTool("browser", "do_thing"); !ok {
		t.Fatalf("enabled plugin tool must be found")
	}
	if !reg.Disable("browser") || len(reg.EnabledTools()) != 0 {
		t.Fatalf("disable should remove tools")
	}
}

func TestBadPluginDoesNotHideGood(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "good")
	bad := filepath.Join(root, "bad-name!")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "plugin.json"), []byte(`{"name": "bad-name!", "description": "x", "tools": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("---\nname: bad-name!\ndescription: x\n---\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(root)
	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 good plugin, got %d", len(reg.List()))
	}
	if len(reg.Errors()) != 1 {
		t.Fatalf("expected 1 error, got %v", reg.Errors())
	}
}

func TestHandlerListsAndEnables(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "browser")
	reg := NewRegistry(root)
	srv := httptest.NewServer(NewHandler(reg))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var listed struct {
		Plugins []Plugin `json:"plugins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Plugins) != 1 || listed.Plugins[0].Name != "browser" {
		t.Fatalf("unexpected plugin list: %+v", listed.Plugins)
	}

	enable, err := http.Post(srv.URL+"/api/plugins/browser/enable", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	enable.Body.Close()
	if enable.StatusCode != http.StatusOK {
		t.Fatalf("enable expected 200, got %d", enable.StatusCode)
	}

	toolsResp, err := http.Get(srv.URL + "/api/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer toolsResp.Body.Close()
	var tools struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.NewDecoder(toolsResp.Body).Decode(&tools); err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Function.Name != "browser.do_thing" {
		t.Fatalf("unexpected tools after enable: %+v", tools.Tools)
	}
}

func TestPersistenceSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "browser")
	state := filepath.Join(t.TempDir(), "state.json")
	first := NewRegistryWithState(root, state)
	if !first.Enable("browser") {
		t.Fatalf("enable should succeed")
	}
	// A new registry, as after a restart, must remember the enable.
	second := NewRegistryWithState(root, state)
	if !second.IsEnabled("browser") {
		t.Fatalf("enabled state was not restored from the state file")
	}
	if len(second.EnabledTools()) != 1 {
		t.Fatalf("restored registry should expose 1 tool, got %d", len(second.EnabledTools()))
	}
	second.Disable("browser")
	third := NewRegistryWithState(root, state)
	if third.IsEnabled("browser") {
		t.Fatalf("disable should be remembered too")
	}
}

func TestRefreshPicksUpNewPlugin(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "browser")
	reg := NewRegistry(root)
	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 plugin at start, got %d", len(reg.List()))
	}
	writePlugin(t, root, "computer")
	reg.Refresh()
	if len(reg.List()) != 2 {
		t.Fatalf("expected 2 plugins after refresh, got %d", len(reg.List()))
	}
	// Refresh must keep enable state for plugins that still exist.
	reg.Enable("browser")
	writePlugin(t, root, "third")
	reg.Refresh()
	if !reg.IsEnabled("browser") {
		t.Fatalf("refresh must keep enable state for existing plugins")
	}
}

func TestCodexPluginWithMCPServers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "github-tools")
	os.MkdirAll(filepath.Join(dir, ".codex-plugin"), 0o755)
	os.MkdirAll(filepath.Join(dir, "skills", "triage"), 0o755)
	os.WriteFile(filepath.Join(dir, ".codex-plugin", "plugin.json"), []byte(`{"name":"github-tools","description":"GitHub helpers.","mcpServers":"./.mcp.json"}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"gh":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/server","args":["--root","${PLUGIN_ROOT}"]}}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "triage", "SKILL.md"), []byte("---\nname: triage\ndescription: Triage issues.\n---\nLabel issues.\n"), 0o644)
	plugins, errs := Discover(root)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors %v", errs)
	}
	p := plugins["github-tools"]
	if p.Format != "codex" || len(p.MCPServers) != 1 {
		t.Fatalf("unexpected plugin %+v", p)
	}
	gh := p.MCPServers["gh"]
	abs, _ := filepath.Abs(dir)
	if gh.Command != abs+"/bin/server" || gh.Args[1] != abs || gh.Cwd != abs {
		t.Fatalf("plugin root not expanded: %+v", gh)
	}
	if len(p.Skills) != 1 || p.Skills[0] != "triage" || !strings.Contains(p.Instructions, "Label issues") {
		t.Fatalf("skills not loaded: %+v %q", p.Skills, p.Instructions)
	}
}

func TestNativePluginWithInlineMCPNeedsNoSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "memory")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"memory","description":"Memory.","mcpServers":{"memory":{"command":"npx","args":["-y","@modelcontextprotocol/server-memory"]}}}`), 0o644)
	plugins, errs := Discover(root)
	if len(errs) != 0 || len(plugins["memory"].MCPServers) != 1 {
		t.Fatalf("inline MCP plugin should load: %v %+v", errs, plugins)
	}
}

func TestMCPPathCannotLeavePlugin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "evil")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"evil","description":"x","mcpServers":"../../etc/x.json"}`), 0o644)
	_, errs := Discover(root)
	if !strings.Contains(errs["evil"], "leaves the plugin folder") {
		t.Fatalf("expected path escape error, got %v", errs)
	}
}
