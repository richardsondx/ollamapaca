//go:build windows || darwin

package tools

// A small, dependency-free Model Context Protocol client.
//
// It supports the three transports MCP servers use in practice, the
// same set Codex and Claude support:
//
//   - stdio: the server is a local command, JSON-RPC messages are
//     newline-delimited JSON on stdin and stdout.
//   - streamable HTTP: every request is a POST to one URL; the reply
//     is either application/json or a text/event-stream carrying the
//     JSON-RPC response.
//   - legacy HTTP+SSE: a GET opens an event stream whose first
//     "endpoint" event names the URL to POST requests to; responses
//     arrive on the stream.
//
// Only the client side of tools is implemented: initialize,
// tools/list and tools/call. Server-to-client requests (ping,
// roots/list, sampling) are answered so servers do not hang.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mcpProtocolVersion = "2025-06-18"
	mcpClientName      = "ollamapaca"
	mcpClientVersion   = "0.1.0"
	// mcpMaxResultChars caps the text handed back to the model. Local
	// models have small context windows, and one huge tool result
	// would push the user's request out of context.
	mcpMaxResultChars = 16000
)

// MCPServerConfig is one MCP server, in the same shape Claude Code,
// Cursor and Codex use in their JSON configs.
type MCPServerConfig struct {
	// Type is "stdio", "http" (streamable HTTP) or "sse" (legacy).
	// Empty means stdio when Command is set and http when URL is set.
	Type     string            `json:"type,omitempty"`
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	// Timeout for one tool call, in seconds. Zero means 60.
	ToolTimeoutSec int `json:"tool_timeout_sec,omitempty"`
}

// Transport returns the effective transport name.
func (c MCPServerConfig) Transport() string {
	switch strings.ToLower(c.Type) {
	case "stdio":
		return "stdio"
	case "sse":
		return "sse"
	case "http", "streamable-http", "streamable_http", "streamablehttp":
		return "http"
	}
	if c.Command != "" {
		return "stdio"
	}
	return "http"
}

// Validate reports a config that can never start.
func (c MCPServerConfig) Validate() error {
	switch c.Transport() {
	case "stdio":
		if strings.TrimSpace(c.Command) == "" {
			return errors.New("a stdio MCP server needs a command")
		}
	default:
		if strings.TrimSpace(c.URL) == "" {
			return errors.New("an HTTP MCP server needs a url")
		}
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("invalid MCP server url %q", c.URL)
		}
	}
	return nil
}

// MCPTool is one tool listed by an MCP server.
type MCPTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("MCP error %d: %s (%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// mcpTransport sends one JSON-RPC request and waits for its response.
type mcpTransport interface {
	request(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	close() error
	// alive is false once the transport can no longer be used.
	alive() bool
}

// MCPClient is a connected, initialized MCP server.
type MCPClient struct {
	Name       string
	Config     MCPServerConfig
	ServerInfo map[string]any
	Instr      string

	t mcpTransport
}

// ConnectMCP starts or connects to a server and runs the initialize
// handshake.
func ConnectMCP(ctx context.Context, name string, cfg MCPServerConfig, logger *slog.Logger) (*MCPClient, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var (
		t   mcpTransport
		err error
	)
	switch cfg.Transport() {
	case "stdio":
		t, err = newStdioTransport(name, cfg, logger)
	case "sse":
		t, err = newSSETransport(ctx, cfg, logger)
	default:
		t = newHTTPTransport(cfg, logger)
	}
	if err != nil {
		return nil, err
	}
	c := &MCPClient{Name: name, Config: cfg, t: t}
	if err := c.initialize(ctx); err != nil {
		// A streamable HTTP URL that refuses POST is most likely an
		// old HTTP+SSE server. Retry once with that transport, the
		// way other MCP clients do.
		if cfg.Transport() == "http" && cfg.Type == "" && isLegacySSEHint(err) {
			_ = t.close()
			st, sseErr := newSSETransport(ctx, cfg, logger)
			if sseErr == nil {
				c.t = st
				if err2 := c.initialize(ctx); err2 == nil {
					return c, nil
				}
				_ = st.close()
			}
		}
		_ = c.t.close()
		return nil, err
	}
	return c, nil
}

func isLegacySSEHint(err error) bool {
	var se *httpStatusError
	if errors.As(err, &se) {
		return se.status == http.StatusNotFound || se.status == http.StatusMethodNotAllowed || se.status == http.StatusBadRequest
	}
	return false
}

func (c *MCPClient) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      map[string]any{"name": mcpClientName, "version": mcpClientVersion},
	}
	raw, err := c.t.request(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("MCP server %q: initialize failed: %w", c.Name, err)
	}
	var res struct {
		ProtocolVersion string         `json:"protocolVersion"`
		ServerInfo      map[string]any `json:"serverInfo"`
		Instructions    string         `json:"instructions"`
	}
	_ = json.Unmarshal(raw, &res)
	c.ServerInfo = res.ServerInfo
	c.Instr = res.Instructions
	if ht, ok := c.t.(*httpTransport); ok && res.ProtocolVersion != "" {
		ht.setProtocolVersion(res.ProtocolVersion)
	}
	return c.t.notify(ctx, "notifications/initialized", nil)
}

// ListTools returns every tool, following pagination.
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	var out []MCPTool
	cursor := ""
	for page := 0; page < 50; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		raw, err := c.t.request(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q: tools/list failed: %w", c.Name, err)
		}
		var res struct {
			Tools      []MCPTool `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("MCP server %q: invalid tools/list reply: %w", c.Name, err)
		}
		out = append(out, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// MCPCallResult is the decoded result of tools/call.
type MCPCallResult struct {
	Text       string `json:"text"`
	IsError    bool   `json:"isError,omitempty"`
	Structured any    `json:"structuredContent,omitempty"`
	Images     int    `json:"images,omitempty"`
}

// CallTool runs one tool.
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (*MCPCallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.t.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	return decodeCallResult(raw)
}

func decodeCallResult(raw json.RawMessage) (*MCPCallResult, error) {
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri"`
			Name     string `json:"name"`
			Resource *struct {
				URI      string `json:"uri"`
				Text     string `json:"text"`
				MimeType string `json:"mimeType"`
				Blob     string `json:"blob"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent any  `json:"structuredContent"`
		IsError           bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("invalid tools/call reply: %w", err)
	}
	out := &MCPCallResult{IsError: res.IsError, Structured: res.StructuredContent}
	var parts []string
	for _, item := range res.Content {
		switch item.Type {
		case "text":
			parts = append(parts, item.Text)
		case "image":
			out.Images++
			parts = append(parts, fmt.Sprintf("[image %s returned by the tool, not shown]", item.MimeType))
		case "audio":
			parts = append(parts, fmt.Sprintf("[audio %s returned by the tool, not shown]", item.MimeType))
		case "resource_link":
			parts = append(parts, fmt.Sprintf("[resource %s %s]", item.Name, item.URI))
		case "resource":
			if item.Resource != nil {
				if item.Resource.Text != "" {
					parts = append(parts, item.Resource.Text)
				} else {
					parts = append(parts, fmt.Sprintf("[resource %s %s]", item.Resource.URI, item.Resource.MimeType))
				}
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	text := strings.Join(parts, "\n")
	if len(text) > mcpMaxResultChars {
		text = text[:mcpMaxResultChars] + "\n[truncated]"
	}
	out.Text = text
	return out, nil
}

// Close stops the server or closes the connection.
func (c *MCPClient) Close() error { return c.t.close() }

// Alive reports whether the connection is still usable.
func (c *MCPClient) Alive() bool { return c.t.alive() }

// ---------------------------------------------------------------
// Request bookkeeping shared by the stdio and SSE transports.

type pendingRequests struct {
	mu      sync.Mutex
	nextID  atomic.Int64
	waiting map[string]chan rpcMessage
	closed  error
}

func newPending() *pendingRequests {
	return &pendingRequests{waiting: map[string]chan rpcMessage{}}
}

func (p *pendingRequests) add() (json.RawMessage, chan rpcMessage, error) {
	id := p.nextID.Add(1)
	key := fmt.Sprint(id)
	ch := make(chan rpcMessage, 1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed != nil {
		return nil, nil, p.closed
	}
	p.waiting[key] = ch
	return json.RawMessage(key), ch, nil
}

func (p *pendingRequests) remove(id json.RawMessage) {
	p.mu.Lock()
	delete(p.waiting, idKey(id))
	p.mu.Unlock()
}

func (p *pendingRequests) deliver(msg rpcMessage) {
	p.mu.Lock()
	ch, ok := p.waiting[idKey(msg.ID)]
	if ok {
		delete(p.waiting, idKey(msg.ID))
	}
	p.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func (p *pendingRequests) failAll(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed == nil {
		p.closed = err
	}
	for k, ch := range p.waiting {
		ch <- rpcMessage{ID: json.RawMessage(k), Error: &rpcError{Code: -32000, Message: err.Error()}}
		delete(p.waiting, k)
	}
}

func idKey(id json.RawMessage) string {
	s := strings.TrimSpace(string(id))
	// Servers may echo a numeric id as a string, treat both the same.
	return strings.Trim(s, `"`)
}

func waitResponse(ctx context.Context, p *pendingRequests, id json.RawMessage, ch chan rpcMessage) (json.RawMessage, error) {
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, msg.Error
		}
		if msg.Result == nil {
			return json.RawMessage("{}"), nil
		}
		return msg.Result, nil
	case <-ctx.Done():
		p.remove(id)
		return nil, ctx.Err()
	}
}

// answerServerRequest builds a reply for a request the server sends
// to the client. Nothing here grants the server anything.
func answerServerRequest(msg rpcMessage) rpcMessage {
	reply := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
	switch msg.Method {
	case "ping":
		reply.Result = json.RawMessage("{}")
	case "roots/list":
		reply.Result = json.RawMessage(`{"roots":[]}`)
	default:
		reply.Error = &rpcError{Code: -32601, Message: "method not supported by this client: " + msg.Method}
	}
	return reply
}

func encodeRequest(id json.RawMessage, method string, params any) ([]byte, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		msg["id"] = json.RawMessage(id)
	}
	if params != nil {
		msg["params"] = params
	}
	return json.Marshal(msg)
}

// ---------------------------------------------------------------
// stdio transport

type stdioTransport struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	pending *pendingRequests
	done    chan struct{}
	stderr  *tailBuffer
	logger  *slog.Logger
}

func newStdioTransport(name string, cfg MCPServerConfig, logger *slog.Logger) (*stdioTransport, error) {
	env := mcpEnv(cfg.Env)
	command := resolveCommand(cfg.Command, env)
	cmd := exec.Command(command, cfg.Args...)
	cmd.Env = env
	if cfg.Cwd != "" {
		cmd.Dir = expandHome(cfg.Cwd)
	}
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{max: 4096}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start MCP server %q (%s): %w", name, cfg.Command, err)
	}
	t := &stdioTransport{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		pending: newPending(),
		done:    make(chan struct{}),
		stderr:  tail,
		logger:  logger,
	}
	go t.readLoop(stdout)
	go func() {
		err := cmd.Wait()
		msg := fmt.Sprintf("MCP server %q exited", name)
		if err != nil {
			msg += ": " + err.Error()
		}
		if s := strings.TrimSpace(tail.String()); s != "" {
			msg += ". stderr: " + s
		}
		t.pending.failAll(errors.New(msg))
		close(t.done)
	}()
	return t, nil
}

func (t *stdioTransport) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		switch {
		case msg.Method != "" && msg.ID != nil:
			reply := answerServerRequest(msg)
			b, _ := json.Marshal(reply)
			_ = t.write(b)
		case msg.Method != "":
			// notification from the server, nothing to do
		default:
			t.pending.deliver(msg)
		}
	}
}

func (t *stdioTransport) write(b []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("MCP server %q: write failed: %w", t.name, err)
	}
	return nil
}

func (t *stdioTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch, err := t.pending.add()
	if err != nil {
		return nil, err
	}
	b, err := encodeRequest(id, method, params)
	if err != nil {
		t.pending.remove(id)
		return nil, err
	}
	if err := t.write(b); err != nil {
		t.pending.remove(id)
		return nil, err
	}
	return waitResponse(ctx, t.pending, id, ch)
}

func (t *stdioTransport) notify(_ context.Context, method string, params any) error {
	b, err := encodeRequest(nil, method, params)
	if err != nil {
		return err
	}
	return t.write(b)
}

func (t *stdioTransport) alive() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

func (t *stdioTransport) close() error {
	_ = t.stdin.Close()
	select {
	case <-t.done:
		return nil
	case <-time.After(2 * time.Second):
	}
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	<-t.done
	return nil
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// ---------------------------------------------------------------
// streamable HTTP transport

type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

type httpTransport struct {
	url       string
	headers   map[string]string
	client    *http.Client
	mu        sync.Mutex
	sessionID string
	protocol  string
	nextID    atomic.Int64
	closed    atomic.Bool
	logger    *slog.Logger
}

func newHTTPTransport(cfg MCPServerConfig, logger *slog.Logger) *httpTransport {
	return &httpTransport{
		url:     cfg.URL,
		headers: expandHeaderEnv(cfg.Headers),
		client:  &http.Client{},
		logger:  logger,
	}
}

func (t *httpTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	t.protocol = v
	t.mu.Unlock()
}

func (t *httpTransport) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", t.protocol)
	}
	t.mu.Unlock()
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, &httpStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return resp, nil
}

func (t *httpTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := json.RawMessage(fmt.Sprint(t.nextID.Add(1)))
	body, err := encodeRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	resp, err := t.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		var result json.RawMessage
		var rerr error
		found := false
		err := readSSE(resp.Body, func(event, data string) bool {
			var msg rpcMessage
			if json.Unmarshal([]byte(data), &msg) != nil {
				return true
			}
			if msg.Method != "" && msg.ID != nil {
				// A server request on the response stream. Reply by POST.
				reply, _ := json.Marshal(answerServerRequest(msg))
				go func() {
					if r, err := t.post(context.Background(), reply); err == nil {
						r.Body.Close()
					}
				}()
				return true
			}
			if msg.Method == "" && idKey(msg.ID) == idKey(id) {
				found = true
				if msg.Error != nil {
					rerr = msg.Error
				} else {
					result = msg.Result
				}
				return false
			}
			return true
		})
		if found {
			if rerr != nil {
				return nil, rerr
			}
			if result == nil {
				result = json.RawMessage("{}")
			}
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		return nil, errors.New("MCP stream ended without a response")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	// Some servers answer with a batch array.
	if len(raw) > 0 && raw[0] == '[' {
		var batch []rpcMessage
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		for _, m := range batch {
			if idKey(m.ID) == idKey(id) {
				if m.Error != nil {
					return nil, m.Error
				}
				return m.Result, nil
			}
		}
		return nil, errors.New("MCP reply did not include a response")
	}
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("MCP reply is not JSON: %w", err)
	}
	if msg.Error != nil {
		return nil, msg.Error
	}
	if msg.Result == nil {
		return json.RawMessage("{}"), nil
	}
	return msg.Result, nil
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	body, err := encodeRequest(nil, method, params)
	if err != nil {
		return err
	}
	resp, err := t.post(ctx, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *httpTransport) alive() bool { return !t.closed.Load() }

func (t *httpTransport) close() error {
	if t.closed.Swap(true) {
		return nil
	}
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
		if err == nil {
			req.Header.Set("Mcp-Session-Id", sid)
			for k, v := range t.headers {
				req.Header.Set(k, v)
			}
			if resp, err := t.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	return nil
}

// readSSE calls fn for each event until fn returns false or the
// stream ends.
func readSSE(r io.Reader, fn func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	event := ""
	var data []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(data) > 0 {
				if !fn(event, strings.Join(data, "\n")) {
					return nil
				}
			}
			event, data = "", nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	if len(data) > 0 {
		fn(event, strings.Join(data, "\n"))
	}
	return sc.Err()
}

// ---------------------------------------------------------------
// legacy HTTP+SSE transport

type sseTransport struct {
	postURL string
	headers map[string]string
	client  *http.Client
	pending *pendingRequests
	cancel  context.CancelFunc
	done    chan struct{}
}

func newSSETransport(ctx context.Context, cfg MCPServerConfig, logger *slog.Logger) (*sseTransport, error) {
	headers := expandHeaderEnv(cfg.Headers)
	streamCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		cancel()
		return nil, &httpStatusError{status: resp.StatusCode, body: string(b)}
	}
	t := &sseTransport{headers: headers, client: client, pending: newPending(), cancel: cancel, done: make(chan struct{})}
	endpoint := make(chan string, 1)
	go func() {
		defer close(t.done)
		defer resp.Body.Close()
		err := readSSE(resp.Body, func(event, data string) bool {
			if event == "endpoint" {
				select {
				case endpoint <- data:
				default:
				}
				return true
			}
			var msg rpcMessage
			if json.Unmarshal([]byte(data), &msg) != nil {
				return true
			}
			switch {
			case msg.Method != "" && msg.ID != nil:
				reply, _ := json.Marshal(answerServerRequest(msg))
				go t.postRaw(context.Background(), reply)
			case msg.Method != "":
			default:
				t.pending.deliver(msg)
			}
			return true
		})
		if err == nil {
			err = errors.New("MCP event stream closed")
		}
		t.pending.failAll(err)
	}()
	select {
	case ep := <-endpoint:
		base, _ := url.Parse(cfg.URL)
		ref, err := url.Parse(strings.TrimSpace(ep))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("invalid SSE endpoint %q", ep)
		}
		t.postURL = base.ResolveReference(ref).String()
	case <-time.After(15 * time.Second):
		cancel()
		return nil, errors.New("MCP SSE server did not send an endpoint event")
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	return t, nil
}

func (t *sseTransport) postRaw(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.postURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &httpStatusError{status: resp.StatusCode, body: string(b)}
	}
	return nil
}

func (t *sseTransport) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch, err := t.pending.add()
	if err != nil {
		return nil, err
	}
	body, err := encodeRequest(id, method, params)
	if err != nil {
		t.pending.remove(id)
		return nil, err
	}
	if err := t.postRaw(ctx, body); err != nil {
		t.pending.remove(id)
		return nil, err
	}
	return waitResponse(ctx, t.pending, id, ch)
}

func (t *sseTransport) notify(ctx context.Context, method string, params any) error {
	body, err := encodeRequest(nil, method, params)
	if err != nil {
		return err
	}
	return t.postRaw(ctx, body)
}

func (t *sseTransport) alive() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

func (t *sseTransport) close() error {
	t.cancel()
	<-t.done
	return nil
}

// ---------------------------------------------------------------
// environment helpers

var (
	loginPathOnce sync.Once
	loginPath     string
)

// userLoginPath returns the PATH of the user's login shell. A macOS
// app started from Finder gets a minimal PATH, so commands like npx,
// uvx or docker that the user installed with Homebrew or nvm would
// not be found without this.
func userLoginPath() string {
	loginPathOnce.Do(func() {
		if runtime.GOOS == "windows" {
			return
		}
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, shell, "-ilc", "printf '__OLLAMAPACA_PATH__%s' \"$PATH\"").Output()
		if err != nil {
			return
		}
		s := string(out)
		if i := strings.LastIndex(s, "__OLLAMAPACA_PATH__"); i >= 0 {
			loginPath = strings.TrimSpace(s[i+len("__OLLAMAPACA_PATH__"):])
		}
	})
	return loginPath
}

func mcpEnv(extra map[string]string) []string {
	env := os.Environ()
	path := os.Getenv("PATH")
	sep := string(os.PathListSeparator)
	parts := strings.Split(path, sep)
	add := func(p string) {
		if p == "" {
			return
		}
		for _, existing := range parts {
			if existing == p {
				return
			}
		}
		parts = append(parts, p)
	}
	for _, p := range strings.Split(userLoginPath(), sep) {
		add(p)
	}
	if runtime.GOOS != "windows" {
		home, _ := os.UserHomeDir()
		for _, p := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin",
			filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".bun", "bin"), filepath.Join(home, ".volta", "bin")} {
			add(p)
		}
	}
	newPath := strings.Join(parts, sep)
	out := make([]string, 0, len(env)+len(extra)+1)
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "PATH="+newPath)
	for k, v := range extra {
		out = append(out, k+"="+os.ExpandEnv(v))
	}
	return out
}

// resolveCommand looks the command up on the augmented PATH, so a
// bare "npx" works from an app started outside a terminal.
func resolveCommand(command string, env []string) string {
	command = expandHome(command)
	if strings.ContainsRune(command, os.PathSeparator) {
		return command
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			for _, dir := range filepath.SplitList(kv[5:]) {
				for _, cand := range candidateNames(command) {
					full := filepath.Join(dir, cand)
					if st, err := os.Stat(full); err == nil && !st.IsDir() {
						return full
					}
				}
			}
		}
	}
	return command
}

func candidateNames(command string) []string {
	if runtime.GOOS == "windows" && filepath.Ext(command) == "" {
		return []string{command + ".exe", command + ".cmd", command + ".bat", command}
	}
	return []string{command}
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func expandHeaderEnv(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = os.ExpandEnv(v)
	}
	return out
}
