//go:build windows || darwin

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// MCPConfigFile is the user's MCP server list, in the format Claude
// Code, Cursor and Codex's JSON importers use:
//
//	{"mcpServers": {"name": {"command": "npx", "args": [...]}}}
type MCPConfigFile struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// DefaultMCPConfigPath is ~/.ollamapaca/mcp.json, or
// OLLAMAPACA_MCP_CONFIG when set.
func DefaultMCPConfigPath() string {
	if v := os.Getenv("OLLAMAPACA_MCP_CONFIG"); v != "" {
		return v
	}
	if v := os.Getenv("OLLAMAPACA_HOME"); v != "" {
		return filepath.Join(v, "mcp.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "mcp.json"
	}
	return filepath.Join(home, ".ollamapaca", "mcp.json")
}

// LoadMCPConfig reads the config file. A missing file is an empty
// config, not an error.
func LoadMCPConfig(path string) (MCPConfigFile, error) {
	cfg := MCPConfigFile{MCPServers: map[string]MCPServerConfig{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid MCP config %s: %w", path, err)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]MCPServerConfig{}
	}
	return cfg, nil
}

// SaveMCPConfig writes the config file atomically.
func SaveMCPConfig(path string, cfg MCPConfigFile) error {
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]MCPServerConfig{}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidMCPServerName reports whether a server name is usable as part
// of a tool name.
func ValidMCPServerName(name string) bool { return mcpNamePattern.MatchString(name) }

var toolNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// MCPServerSpec is one server the manager should run. Key is unique
// across sources: "name" for user servers, "plugin/name" for servers
// bundled with a plugin.
type MCPServerSpec struct {
	Key    string
	Name   string
	Plugin string // empty for user servers
	Config MCPServerConfig
	// SetupError, when set, says which plugin settings are missing.
	// The server is not started until they are filled in.
	SetupError string
}

// MCPServerStatus is what the Plugins page shows for a server.
type MCPServerStatus struct {
	Key       string          `json:"key"`
	Name      string          `json:"name"`
	Plugin    string          `json:"plugin,omitempty"`
	Config    MCPServerConfig `json:"config"`
	Transport string          `json:"transport"`
	State     string          `json:"state"` // idle, starting, ready, error, disabled, needs_auth, needs_setup
	Error     string          `json:"error,omitempty"`
	Tools     []MCPTool       `json:"tools"`
	Server    map[string]any  `json:"serverInfo,omitempty"`
	// SignedIn is true when an OAuth token is stored for the server.
	SignedIn bool `json:"signedIn,omitempty"`
}

type mcpServerState struct {
	spec      MCPServerSpec
	mu        sync.Mutex // serializes connects
	client    *MCPClient
	tools     []MCPTool
	state     string
	err       string
	lastFail  time.Time
	connected time.Time
}

// MCPManager owns every MCP connection for the app. Servers start on
// first use, are kept running between chats, and restart after a
// crash. Close stops them all.
type MCPManager struct {
	mu      sync.Mutex
	servers map[string]*mcpServerState
	logger  *slog.Logger
}

// NewMCPManager returns an empty manager.
func NewMCPManager(logger *slog.Logger) *MCPManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &MCPManager{servers: map[string]*mcpServerState{}, logger: logger}
}

// Sync makes the running set match specs: new servers are added (not
// started), removed or changed servers are stopped.
func (m *MCPManager) Sync(specs []MCPServerSpec) {
	want := map[string]MCPServerSpec{}
	for _, s := range specs {
		want[s.Key] = s
	}
	var toClose []*MCPClient
	m.mu.Lock()
	for key, st := range m.servers {
		spec, ok := want[key]
		if !ok || !reflect.DeepEqual(spec.Config, st.spec.Config) || spec.SetupError != st.spec.SetupError {
			if st.client != nil {
				toClose = append(toClose, st.client)
			}
			delete(m.servers, key)
		}
	}
	for key, spec := range want {
		if _, ok := m.servers[key]; !ok {
			state := "idle"
			if spec.Config.Disabled {
				state = "disabled"
			} else if spec.SetupError != "" {
				state = "needs_setup"
			}
			m.servers[key] = &mcpServerState{spec: spec, state: state}
		}
	}
	m.mu.Unlock()
	for _, c := range toClose {
		go c.Close()
	}
}

func (m *MCPManager) get(key string) (*mcpServerState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.servers[key]
	return st, ok
}

// connect returns a live client, starting or restarting the server.
func (m *MCPManager) connect(ctx context.Context, st *mcpServerState) (*MCPClient, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.spec.Config.Disabled {
		return nil, fmt.Errorf("MCP server %q is disabled", st.spec.Name)
	}
	if st.spec.SetupError != "" {
		st.state = "needs_setup"
		st.err = st.spec.SetupError
		return nil, errors.New(st.spec.SetupError)
	}
	if st.client != nil && st.client.Alive() {
		return st.client, nil
	}
	if st.client != nil {
		go st.client.Close()
		st.client = nil
	}
	st.state = "starting"
	// npx and uvx download the server on first run, so allow time.
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := ConnectMCP(cctx, st.spec.Name, st.spec.Config, m.logger)
	if err == nil {
		var tools []MCPTool
		tools, err = client.ListTools(cctx)
		if err == nil {
			st.client = client
			st.tools = tools
			st.state = "ready"
			st.err = ""
			st.connected = time.Now()
			m.logger.Info("MCP server ready", "server", st.spec.Key, "tools", len(tools))
			return client, nil
		}
		client.Close()
	}
	st.state = "error"
	st.err = err.Error()
	if errors.Is(err, ErrNeedsSignIn) {
		st.state = "needs_auth"
		st.err = "Sign in to use this server: click Connect."
	}
	st.lastFail = time.Now()
	m.logger.Warn("MCP server failed", "server", st.spec.Key, "error", err)
	return nil, err
}

// Tools returns the tools of one server, connecting if needed.
func (m *MCPManager) Tools(ctx context.Context, key string) ([]MCPTool, error) {
	st, ok := m.get(key)
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", key)
	}
	if _, err := m.connect(ctx, st); err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]MCPTool(nil), st.tools...), nil
}

// Restart stops a server; the next use starts it again.
func (m *MCPManager) Restart(ctx context.Context, key string) ([]MCPTool, error) {
	st, ok := m.get(key)
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", key)
	}
	st.mu.Lock()
	if st.client != nil {
		st.client.Close()
		st.client = nil
	}
	st.mu.Unlock()
	return m.Tools(ctx, key)
}

// Call runs one tool. A dead connection is restarted once.
func (m *MCPManager) Call(ctx context.Context, key, tool string, args map[string]any) (*MCPCallResult, error) {
	st, ok := m.get(key)
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", key)
	}
	timeout := time.Duration(st.spec.Config.ToolTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := m.connect(ctx, st)
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		res, err := client.CallTool(cctx, tool, args)
		cancel()
		if err == nil {
			return res, nil
		}
		if client.Alive() || ctx.Err() != nil {
			return nil, err
		}
		m.logger.Warn("MCP server died during a call, restarting", "server", key, "error", err)
	}
	return nil, fmt.Errorf("MCP server %q is not responding", st.spec.Name)
}

// Status lists every server for the UI, without starting any.
func (m *MCPManager) Status() []MCPServerStatus {
	m.mu.Lock()
	states := make([]*mcpServerState, 0, len(m.servers))
	for _, st := range m.servers {
		states = append(states, st)
	}
	m.mu.Unlock()
	out := make([]MCPServerStatus, 0, len(states))
	for _, st := range states {
		st.mu.Lock()
		s := MCPServerStatus{
			Key:       st.spec.Key,
			Name:      st.spec.Name,
			Plugin:    st.spec.Plugin,
			Config:    redactConfig(st.spec.Config),
			Transport: st.spec.Config.Transport(),
			State:     st.state,
			Error:     st.err,
			Tools:     append([]MCPTool{}, st.tools...),
		}
		if st.client != nil {
			s.Server = st.client.ServerInfo
		}
		if s.Transport != "stdio" {
			s.SignedIn = MCPSignedIn(st.spec.Config.URL)
		}
		if st.spec.SetupError != "" {
			s.Error = st.spec.SetupError
		}
		st.mu.Unlock()
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// redactConfig hides header and env values, which often hold tokens.
func redactConfig(c MCPServerConfig) MCPServerConfig {
	if len(c.Env) > 0 {
		env := make(map[string]string, len(c.Env))
		for k := range c.Env {
			env[k] = "••••"
		}
		c.Env = env
	}
	if len(c.Headers) > 0 {
		h := make(map[string]string, len(c.Headers))
		for k := range c.Headers {
			h[k] = "••••"
		}
		c.Headers = h
	}
	return c
}

// SignIn starts a browser OAuth sign-in for an HTTP server. It
// returns the URL to open; when the sign-in finishes the server is
// reconnected in the background.
func (m *MCPManager) SignIn(ctx context.Context, key string) (string, error) {
	st, ok := m.get(key)
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", key)
	}
	if st.spec.Config.Transport() == "stdio" {
		return "", errors.New("sign-in is only for remote MCP servers")
	}
	login, err := StartMCPOAuthLogin(ctx, st.spec.Config.URL, st.spec.Config.OAuth)
	if err != nil {
		return "", err
	}
	go func() {
		wctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := login.Wait(wctx); err != nil {
			st.mu.Lock()
			st.state = "needs_auth"
			st.err = err.Error()
			st.mu.Unlock()
			return
		}
		_, _ = m.Restart(context.Background(), key)
	}()
	return login.AuthURL, nil
}

// SignOut forgets the stored token and disconnects the server.
func (m *MCPManager) SignOut(key string) error {
	st, ok := m.get(key)
	if !ok {
		return fmt.Errorf("unknown MCP server %q", key)
	}
	if err := MCPSignOut(st.spec.Config.URL); err != nil {
		return err
	}
	st.mu.Lock()
	if st.client != nil {
		st.client.Close()
		st.client = nil
	}
	st.tools = nil
	st.state = "needs_auth"
	st.err = ""
	st.mu.Unlock()
	return nil
}

// Close stops every server.
func (m *MCPManager) Close() {
	m.mu.Lock()
	var clients []*MCPClient
	for _, st := range m.servers {
		if st.client != nil {
			clients = append(clients, st.client)
			st.client = nil
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *MCPClient) {
			defer wg.Done()
			c.Close()
		}(c)
	}
	wg.Wait()
}

// MCPToolAdapter exposes one MCP tool through the chat Tool interface.
type MCPToolAdapter struct {
	manager  *MCPManager
	key      string
	toolName string
	fullName string
	desc     string
	schema   map[string]any
}

// MCPToolName is the name the model sees for a tool: plugin.tool for
// plugin-bundled servers, mcp__server__tool for user servers, the
// same convention Claude Code uses.
func MCPToolName(spec MCPServerSpec, tool string) string {
	t := toolNameSanitizer.ReplaceAllString(tool, "_")
	if spec.Plugin != "" {
		return spec.Plugin + "." + t
	}
	return "mcp__" + spec.Name + "__" + t
}

// AdaptersFor connects to a server and returns its tools as chat
// tools.
func (m *MCPManager) AdaptersFor(ctx context.Context, key string) ([]*MCPToolAdapter, error) {
	st, ok := m.get(key)
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q", key)
	}
	tools, err := m.Tools(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]*MCPToolAdapter, 0, len(tools))
	for _, t := range tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if _, ok := schema["type"]; !ok {
			schema["type"] = "object"
		}
		desc := t.Description
		if desc == "" {
			desc = t.Title
		}
		if desc == "" {
			desc = t.Name
		}
		desc = shortenDescription(desc, 1000)
		out = append(out, &MCPToolAdapter{
			manager:  m,
			key:      key,
			toolName: t.Name,
			fullName: MCPToolName(st.spec, t.Name),
			desc:     desc,
			schema:   schema,
		})
	}
	return out, nil
}

func (a *MCPToolAdapter) Name() string           { return a.fullName }
func (a *MCPToolAdapter) Description() string    { return a.desc }
func (a *MCPToolAdapter) Schema() map[string]any { return a.schema }
func (a *MCPToolAdapter) Prompt() string         { return "" }

// ServerKey is the manager key of the server behind this tool.
func (a *MCPToolAdapter) ServerKey() string { return a.key }

// Execute calls the tool. An isError result is returned as an error,
// so the chat loop records a failed step instead of a success.
func (a *MCPToolAdapter) Execute(ctx context.Context, args map[string]any) (any, string, error) {
	res, err := a.manager.Call(ctx, a.key, a.toolName, args)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", a.fullName, err)
	}
	if res.IsError {
		msg := strings.TrimSpace(res.Text)
		if msg == "" {
			msg = "the tool reported an error"
		}
		return nil, "", fmt.Errorf("%s: %s", a.fullName, msg)
	}
	text := res.Text
	if text == "" {
		text = "(the tool returned no content)"
	}
	return map[string]any{"text": text, "structuredContent": res.Structured}, text, nil
}

// shortenDescription keeps tool descriptions within what small local
// models handle: long descriptions make them write tool calls that
// Ollama cannot parse. It cuts at a paragraph or line boundary.
func shortenDescription(desc string, max int) string {
	desc = strings.TrimSpace(desc)
	if len(desc) <= max {
		return desc
	}
	cut := desc[:max]
	if i := strings.LastIndex(cut, "\n\n"); i > max/3 {
		cut = cut[:i]
	} else if i := strings.LastIndex(cut, "\n"); i > max/3 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}
