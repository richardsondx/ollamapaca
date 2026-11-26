// Package plugins loads Codex-style plugins for Ollama.
//
// A plugin is a folder with a plugin.json manifest and a SKILL.md skill.
// This package is standard library only, so it can be tested without
// building the full Ollama server. It is a port of the Python seed
// loader, not a copy of any OpenAI code.
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tool is one tool declared in a plugin manifest.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// MCPServer is an MCP server bundled with a plugin, in the shape
// Codex and Claude plugins use in .mcp.json. The desktop app starts
// it and exposes its tools as the plugin's tools.
type MCPServer struct {
	Type           string            `json:"type,omitempty"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Disabled       bool              `json:"disabled,omitempty"`
	ToolTimeoutSec int               `json:"tool_timeout_sec,omitempty"`
	// OAuth turns on MCP OAuth sign-in for an HTTP server. With no
	// client id, the app registers itself with the authorization
	// server (dynamic client registration).
	OAuth *OAuthConfig `json:"oauth,omitempty"`
}

// OAuthConfig is the OAuth client a plugin's MCP server signs in with.
// Values may reference plugin settings as ${NAME}.
type OAuthConfig struct {
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// Setting is a value the user enters on the Plugins page, such as an
// API key. MCP server commands, URLs, headers and env reference it as
// ${NAME}.
type Setting struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Help     string `json:"help,omitempty"`
	URL      string `json:"url,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Plugin is a validated plugin folder.
type Plugin struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Version     string               `json:"version"`
	Tools       []Tool               `json:"tools"`
	Skills      []string             `json:"skills"`
	Enabled     bool                 `json:"enabled"`
	MCPServers  map[string]MCPServer `json:"mcpServers,omitempty"`
	// Dir is the plugin folder on disk.
	Dir string `json:"dir,omitempty"`
	// Format is "ollamapaca", "codex" or "claude", from where the
	// manifest was found.
	Format string `json:"format,omitempty"`
	// Instructions is the SKILL.md body, given to the model as a
	// system message while the plugin's tools are active.
	Instructions string `json:"-"`
	// Settings the user can fill in, and where to learn more.
	Settings []Setting `json:"settings,omitempty"`
	Homepage string    `json:"homepage,omitempty"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

var settingNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// parseFrontmatter reads the simple key: value frontmatter at the
// top of a SKILL.md file. It is not a full YAML parser, by design:
// skill frontmatter in this seed uses only flat keys.
func parseFrontmatter(text string) (map[string]string, string, error) {
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return nil, "", fmt.Errorf("missing frontmatter")
	}
	rest := text[4:]
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("unterminated frontmatter")
	}
	out := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	body := rest[end+4:]
	return out, body, nil
}

// manifestLocations are checked in order. plugin.json at the root
// is the native format; the other two are the Codex and Claude
// plugin layouts, so plugins written for those apps load here too.
var manifestLocations = []struct {
	path   string
	format string
}{
	{"plugin.json", "ollamapaca"},
	{filepath.Join(".codex-plugin", "plugin.json"), "codex"},
	{filepath.Join(".claude-plugin", "plugin.json"), "claude"},
}

func loadPlugin(dir string) (Plugin, error) {
	var p Plugin
	var data []byte
	format := ""
	for _, loc := range manifestLocations {
		b, err := os.ReadFile(filepath.Join(dir, loc.path))
		if err == nil {
			data, format = b, loc.format
			break
		}
	}
	if data == nil {
		return p, fmt.Errorf("missing plugin.json in %s", dir)
	}
	var raw struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Version     string          `json:"version"`
		Tools       []Tool          `json:"tools"`
		MCPServers  json.RawMessage `json:"mcpServers"`
		Settings    []Setting       `json:"settings"`
		Homepage    string          `json:"homepage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return p, fmt.Errorf("invalid plugin.json in %s: %w", dir, err)
	}
	if !namePattern.MatchString(raw.Name) {
		return p, fmt.Errorf("invalid plugin name %q", raw.Name)
	}
	if format == "ollamapaca" && filepath.Base(dir) != raw.Name {
		return p, fmt.Errorf("folder name %q does not match plugin name %q", filepath.Base(dir), raw.Name)
	}
	servers, err := loadMCPServers(dir, raw.MCPServers)
	if err != nil {
		return p, fmt.Errorf("plugin %q: %w", raw.Name, err)
	}
	skillNames, instructions, skillDesc, err := loadSkills(dir, raw.Name, format, len(servers) > 0)
	if err != nil {
		return p, err
	}
	if raw.Description == "" {
		raw.Description = skillDesc
	}
	if raw.Description == "" {
		return p, fmt.Errorf("missing description for plugin %q", raw.Name)
	}
	if raw.Tools == nil && len(servers) == 0 && len(skillNames) == 0 {
		return p, fmt.Errorf("missing tools for plugin %q", raw.Name)
	}
	for _, t := range raw.Tools {
		if t.Name == "" || t.Description == "" {
			return p, fmt.Errorf("tool in plugin %q is missing a name or description", raw.Name)
		}
		if t.Parameters == nil {
			return p, fmt.Errorf("tool %q in plugin %q is missing parameters", t.Name, raw.Name)
		}
		if typ, _ := t.Parameters["type"].(string); typ != "object" {
			return p, fmt.Errorf("tool %q in plugin %q must have object parameters", t.Name, raw.Name)
		}
	}
	if raw.Tools == nil {
		raw.Tools = []Tool{}
	}
	for _, st := range raw.Settings {
		if !settingNamePattern.MatchString(st.Name) {
			return p, fmt.Errorf("plugin %q: invalid setting name %q, use A-Z, 0-9 and _", raw.Name, st.Name)
		}
	}
	abs, _ := filepath.Abs(dir)
	p = Plugin{
		Name:         raw.Name,
		Description:  raw.Description,
		Version:      raw.Version,
		Tools:        raw.Tools,
		Skills:       skillNames,
		MCPServers:   servers,
		Dir:          abs,
		Format:       format,
		Instructions: instructions,
		Settings:     raw.Settings,
		Homepage:     raw.Homepage,
	}
	return p, nil
}

// loadSkills reads SKILL.md at the plugin root (required for the
// native format unless the plugin bundles MCP servers) and any
// skills/<name>/SKILL.md files (the Codex and Claude layout).
func loadSkills(dir, name, format string, hasMCP bool) ([]string, string, string, error) {
	var names []string
	var bodies []string
	desc := ""
	rootSkill, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err == nil {
		front, body, err := parseFrontmatter(string(rootSkill))
		if err != nil {
			return nil, "", "", fmt.Errorf("invalid SKILL.md in %s: %w", dir, err)
		}
		if front["name"] != name {
			return nil, "", "", fmt.Errorf("SKILL.md name %q does not match plugin name %q", front["name"], name)
		}
		if front["description"] == "" {
			return nil, "", "", fmt.Errorf("SKILL.md for plugin %q is missing a description", name)
		}
		names = append(names, name)
		bodies = append(bodies, strings.TrimSpace(body))
		desc = front["description"]
	} else if format == "ollamapaca" && !hasMCP {
		return nil, "", "", fmt.Errorf("missing SKILL.md in %s", dir)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "skills"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "skills", e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		front, body, err := parseFrontmatter(string(b))
		if err != nil {
			continue
		}
		skill := front["name"]
		if skill == "" {
			skill = e.Name()
		}
		names = append(names, skill)
		header := "## Skill: " + skill
		if d := front["description"]; d != "" {
			header += "\n" + d
			if desc == "" {
				desc = d
			}
		}
		bodies = append(bodies, header+"\n\n"+strings.TrimSpace(body))
	}
	if names == nil {
		names = []string{}
	}
	return names, strings.Join(bodies, "\n\n"), desc, nil
}

// loadMCPServers reads mcpServers from the manifest, which may be an
// inline object or a path to a JSON file, or from .mcp.json in the
// plugin folder. A file may hold {"mcpServers": {...}} or the server
// map itself.
func loadMCPServers(dir string, field json.RawMessage) (map[string]MCPServer, error) {
	var fileData []byte
	trimmed := strings.TrimSpace(string(field))
	switch {
	case trimmed == "" || trimmed == "null":
		b, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
		if err != nil {
			return nil, nil
		}
		fileData = b
	case strings.HasPrefix(trimmed, "\""):
		var rel string
		if err := json.Unmarshal(field, &rel); err != nil {
			return nil, err
		}
		full := filepath.Join(dir, filepath.Clean(rel))
		if !strings.HasPrefix(full, filepath.Clean(dir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("mcpServers path %q leaves the plugin folder", rel)
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("cannot read mcpServers file %q: %w", rel, err)
		}
		fileData = b
	default:
		fileData = field
	}
	var wrapped struct {
		MCPServers map[string]MCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(fileData, &wrapped); err == nil && wrapped.MCPServers != nil {
		return expandPluginRoot(dir, wrapped.MCPServers), nil
	}
	var bare map[string]MCPServer
	if err := json.Unmarshal(fileData, &bare); err != nil {
		return nil, fmt.Errorf("invalid mcpServers: %w", err)
	}
	return expandPluginRoot(dir, bare), nil
}

// expandPluginRoot replaces the plugin root placeholders that Claude
// and Codex plugins use in commands and arguments.
func expandPluginRoot(dir string, servers map[string]MCPServer) map[string]MCPServer {
	abs, _ := filepath.Abs(dir)
	r := strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", abs, "${PLUGIN_ROOT}", abs, "${CODEX_PLUGIN_ROOT}", abs)
	out := make(map[string]MCPServer, len(servers))
	for name, s := range servers {
		s.Command = r.Replace(s.Command)
		s.Cwd = r.Replace(s.Cwd)
		args := make([]string, len(s.Args))
		for i, a := range s.Args {
			args[i] = r.Replace(a)
		}
		s.Args = args
		env := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			env[k] = r.Replace(v)
		}
		s.Env = env
		if s.Cwd == "" && s.Command != "" {
			s.Cwd = abs
		}
		out[name] = s
	}
	return out
}

// Discover loads every plugin folder under root. A bad plugin is
// reported in errors and does not hide the good plugins.
func Discover(root string) (map[string]Plugin, map[string]string) {
	plugins := map[string]Plugin{}
	errors := map[string]string{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return plugins, errors
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "__pycache__" {
			continue
		}
		p, err := loadPlugin(filepath.Join(root, e.Name()))
		if err != nil {
			errors[e.Name()] = err.Error()
			continue
		}
		plugins[p.Name] = p
	}
	return plugins, errors
}

// DiscoverAll loads plugins from several folders. When two folders
// hold a plugin with the same name, the first folder wins, so a user
// folder listed first can override a bundled plugin.
func DiscoverAll(roots []string) (map[string]Plugin, map[string]string) {
	plugins := map[string]Plugin{}
	errors := map[string]string{}
	for _, root := range roots {
		ps, errs := Discover(root)
		for name, p := range ps {
			if _, ok := plugins[name]; !ok {
				plugins[name] = p
			}
		}
		for k, v := range errs {
			if _, ok := errors[k]; !ok {
				errors[k] = v
			}
		}
	}
	return plugins, errors
}

// SortedNames returns plugin names in alphabetical order.
func SortedNames(plugins map[string]Plugin) []string {
	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
