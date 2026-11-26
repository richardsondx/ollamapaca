//go:build windows || darwin

package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/app/tools"
)

func TestCompactOldBrowserResults(t *testing.T) {
	page := func(title string) string {
		return "Tab 1: " + title + "\nURL: https://x\n\nInteractive elements (use the number):\n" + strings.Repeat("[1] link \"a\"\n", 100)
	}
	msgs := []api.Message{
		{Role: "user", Content: "go"},
		{Role: "tool", ToolName: "browser.navigate", Content: page("old")},
		{Role: "tool", ToolName: "mcp__fs__read", Content: strings.Repeat("x", 2000)},
		{Role: "tool", ToolName: "browser.click", Content: page("new")},
	}
	compactOldBrowserResults(msgs)
	if !strings.Contains(msgs[1].Content, "trimmed") || strings.Contains(msgs[1].Content, "[1] link") {
		t.Fatalf("old snapshot not trimmed: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "Tab 1: old") {
		t.Fatalf("trimmed snapshot must keep its header: %q", msgs[1].Content)
	}
	if len(msgs[2].Content) != 2000 {
		t.Fatalf("non-browser results must be left alone")
	}
	if !strings.Contains(msgs[3].Content, "[1] link") {
		t.Fatalf("newest snapshot must be kept whole")
	}
}

func TestBuiltinBrowserToolNames(t *testing.T) {
	for _, n := range []string{"browser.search", "browser.open", "browser.find"} {
		if !isBuiltinBrowserTool(n) {
			t.Fatalf("%s is built in", n)
		}
	}
	for _, n := range []string{"browser.click", "browser.create_tab", "browser.webmcp.list_tools"} {
		if isBuiltinBrowserTool(n) {
			t.Fatalf("%s is a plugin tool", n)
		}
	}
}

func TestRegisterTurnToolsMentionRules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OLLAMAPACA_HOME", home)
	t.Setenv("OLLAMA_PLUGINS_DIR", filepath.Join(home, "plugins"))
	dir := filepath.Join(home, "plugins", "notes")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"notes","description":"Notes.","tools":[{"name":"add","description":"Add a note.","parameters":{"type":"object","properties":{}}}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: notes\ndescription: Notes.\n---\nUse add to save notes.\n"), 0o644)

	svc := newPluginService(nil)
	defer svc.mcp.Close()
	if !svc.reg.Enable("notes") {
		t.Fatalf("enable failed: %v", svc.reg.Errors())
	}
	reg := tools.NewRegistry()
	turn := svc.registerTurnTools(t.Context(), reg, "save this")
	if _, ok := reg.Get("notes.add"); !ok || len(turn.instructions) != 1 {
		t.Fatalf("no mention: enabled plugin tools and skill expected, got %v %v", reg.ToolNames(), turn.instructions)
	}
	reg = tools.NewRegistry()
	svc.registerTurnTools(t.Context(), reg, "@browser open a page")
	if _, ok := reg.Get("notes.add"); ok {
		t.Fatalf("a mention of another plugin must not register notes")
	}
}
