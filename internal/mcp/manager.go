package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

// HandshakeTimeout bounds MCP connection and discovery setup for one server.
const HandshakeTimeout = 20 * time.Second

var errMCPTool = errors.New("MCP tool error")

// Notification is runtime state that the server persists and publishes.
type Notification struct {
	Kind    string
	Server  string
	Message string
}

// NotifyFunc receives asynchronous MCP catalog notifications for a session.
type NotifyFunc func(sessionID string, notification Notification)

// Manager owns live SDK sessions. Tool definitions are intentionally absent:
// they are immutable session resources managed by internal/resources.
type Manager struct {
	mu       sync.Mutex
	by       map[string]map[string]*connection
	notify   NotifyFunc
	clientID *mcp.Implementation
}

type connection struct {
	ready   chan struct{}
	session *mcp.ClientSession
	cancel  context.CancelFunc
	err     error
	closed  bool
}

// PrepareResult contains one prompt's successfully bound tools and discovery
// updates. A failure only removes that server from this prompt.
type PrepareResult struct {
	Tools  []loop.Tool
	States map[string]ServerState
}

// NewManager creates a manager for session-scoped MCP connections.
func NewManager(notify NotifyFunc) *Manager {
	return &Manager{
		by:       map[string]map[string]*connection{},
		notify:   notify,
		clientID: &mcp.Implementation{Name: "ki", Version: "0.1"},
	}
}

// Prepare ensures every enabled server is connected before prompt assembly.
// Servers are independent and therefore loaded in parallel under per-server
// timeouts rather than accumulating timeout latency.
func (m *Manager) Prepare(ctx context.Context, sessionID string, file File, toggle session.Toggle, cached map[string]ServerState) PrepareResult {
	result := PrepareResult{States: map[string]ServerState{}}
	type item struct {
		name  string
		state ServerState
		tools []loop.Tool
	}
	ch := make(chan item, len(file.MCPServers))
	var wg sync.WaitGroup
	names := make([]string, 0, len(file.MCPServers))
	for name := range file.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := file.MCPServers[name]
		if !toggle.Allowed(name) {
			continue
		}
		wg.Go(func() {
			loadCtx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
			defer cancel()
			state, tools := m.prepareServer(loadCtx, sessionID, name, spec, file.Sources[name], cached[name])
			ch <- item{name: name, state: state, tools: tools}
		})
	}
	wg.Wait()
	close(ch)
	items := map[string]item{}
	for got := range ch {
		items[got.name] = got
	}
	for _, name := range names {
		got, ok := items[name]
		if !ok {
			continue
		}
		result.States[got.name] = got.state
		result.Tools = append(result.Tools, got.tools...)
	}
	return result
}

func (m *Manager) prepareServer(ctx context.Context, sessionID, name string, spec ServerSpec, source string, cached ServerState) (ServerState, []loop.Tool) {
	if err := ValidateServerSpec(spec); err != nil {
		return failedState(err), nil
	}
	conn, fresh, err := m.ensure(ctx, sessionID, name, spec)
	if err != nil {
		return failedState(err), nil
	}
	state := cached
	// A list-changed notification is advisory until the user explicitly
	// reloads. Reconnecting must not silently replace the pinned schema.
	if state.Status != StatusStale && (fresh || state.Status != StatusReady) {
		state, err = discover(ctx, conn.session)
		if err != nil {
			m.drop(sessionID, name, conn)
			return failedState(err), nil
		}
	}
	state.Error = ""
	if state.Status != StatusStale {
		state.Status = StatusReady
	}
	state.Tools = applyExtensionToolNames(state.Tools, source)
	tools := make([]loop.Tool, 0, len(state.Tools))
	for _, definition := range state.Tools {
		tools = append(tools, sdkTool{manager: m, conn: conn, sessionID: sessionID, server: name, definition: definition})
	}
	return state, tools
}

func applyExtensionToolNames(defs []ToolDefinition, source string) []ToolDefinition {
	const p = "extension:"
	if !strings.HasPrefix(source, p) {
		return defs
	}
	ext := strings.TrimPrefix(source, p)
	if ext == "" {
		return defs
	}
	out := make([]ToolDefinition, 0, len(defs))
	seen := map[string]bool{}
	for _, def := range defs {
		wire := def.Name
		if def.WireName != "" {
			wire = def.WireName
		}
		if strings.Contains(wire, "/") {
			continue
		}
		name := ext + "/" + wire
		if seen[name] {
			continue
		}
		seen[name] = true
		def.WireName = wire
		def.Name = name
		out = append(out, def)
	}
	return out
}

func failedState(err error) ServerState {
	return ServerState{Status: StatusFailed, Error: err.Error(), LoadedAt: time.Now().UTC()}
}

func discover(ctx context.Context, cs *mcp.ClientSession) (ServerState, error) {
	state := ServerState{Status: StatusReady, LoadedAt: time.Now().UTC()}
	if init := cs.InitializeResult(); init != nil {
		state.ProtocolVersion = init.ProtocolVersion
		if init.ServerInfo != nil {
			state.ServerName = init.ServerInfo.Name
			state.ServerVersion = init.ServerInfo.Version
		}
		capabilities, err := json.Marshal(init.Capabilities)
		if err != nil {
			return ServerState{}, fmt.Errorf("marshal MCP capabilities: %w", err)
		}
		state.Capabilities = capabilities
	}
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return ServerState{}, fmt.Errorf("list MCP tools: %w", err)
		}
		definition, err := copyTool(tool)
		if err != nil {
			return ServerState{}, err
		}
		state.Tools = append(state.Tools, definition)
	}
	return state, nil
}

func copyTool(tool *mcp.Tool) (ToolDefinition, error) {
	raw, err := json.Marshal(tool)
	if err != nil {
		return ToolDefinition{}, fmt.Errorf("copy MCP tool %q: %w", tool.Name, err)
	}
	var wire struct {
		Name         string         `json:"name"`
		Title        string         `json:"title"`
		Description  string         `json:"description"`
		InputSchema  map[string]any `json:"inputSchema"`
		OutputSchema any            `json:"outputSchema"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return ToolDefinition{}, fmt.Errorf("decode MCP tool %q: %w", tool.Name, err)
	}
	if wire.InputSchema == nil {
		wire.InputSchema = map[string]any{"type": "object"}
	}
	return ToolDefinition{
		Name: wire.Name, Title: wire.Title, Description: wire.Description,
		InputSchema: wire.InputSchema, OutputSchema: wire.OutputSchema, Raw: raw,
	}, nil
}

func (m *Manager) ensure(ctx context.Context, sessionID, name string, spec ServerSpec) (*connection, bool, error) {
	m.mu.Lock()
	servers := m.by[sessionID]
	if servers == nil {
		servers = map[string]*connection{}
		m.by[sessionID] = servers
	}
	if current := servers[name]; current != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-current.ready:
			return current, false, current.err
		}
	}
	connectCtx, cancel := context.WithCancel(ctx)
	conn := &connection{ready: make(chan struct{}), cancel: cancel}
	servers[name] = conn
	m.mu.Unlock()

	cs, err := m.connect(connectCtx, sessionID, name, spec, conn)
	m.mu.Lock()
	conn.cancel = nil
	if err == nil && (conn.closed || m.by[sessionID] == nil || m.by[sessionID][name] != conn) {
		conn.err = context.Canceled
		close(conn.ready)
		m.mu.Unlock()
		_ = cs.Close()
		return conn, true, context.Canceled
	}
	if err != nil {
		conn.err = err
		if m.by[sessionID][name] == conn {
			delete(m.by[sessionID], name)
		}
	} else {
		conn.session = cs
	}
	close(conn.ready)
	m.mu.Unlock()
	return conn, true, err
}

func (m *Manager) connect(ctx context.Context, sessionID, name string, spec ServerSpec, conn *connection) (*mcp.ClientSession, error) {
	client := mcp.NewClient(m.clientID, &mcp.ClientOptions{
		Capabilities:   &mcp.ClientCapabilities{},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			m.mu.Lock()
			active := !conn.closed && m.by[sessionID] != nil && m.by[sessionID][name] == conn
			m.mu.Unlock()
			if active && m.notify != nil {
				m.notify(sessionID, Notification{Kind: "tools_changed", Server: name, Message: "tool list changed; reload required"})
			}
		},
	})
	var transport mcp.Transport
	if spec.URL != "" {
		hc := &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: spec.Headers}}
		transport = &mcp.StreamableClientTransport{Endpoint: spec.URL, HTTPClient: hc}
	} else {
		// The command and arguments are explicit MCP configuration supplied by
		// the user or workspace owner.
		cmd := exec.CommandContext(ctx, spec.Command, spec.Args...) //nolint:gosec // MCP command configuration is the feature contract
		cmd.Stderr = os.Stderr
		if len(spec.Env) > 0 {
			cmd.Env = os.Environ()
			for key, value := range spec.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		transport = &mcp.CommandTransport{Command: cmd}
	}
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP server %q: %w", name, err)
	}
	return cs, nil
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, value := range t.headers {
		clone.Header.Set(key, value)
	}
	return t.base.RoundTrip(clone)
}

func (m *Manager) drop(sessionID, name string, expected *connection) {
	m.mu.Lock()
	if servers := m.by[sessionID]; servers != nil && servers[name] == expected {
		delete(servers, name)
	}
	expected.closed = true
	cs := expected.session
	m.mu.Unlock()
	if cs != nil {
		_ = cs.Close()
	}
}

// CloseSession releases all live connections owned by one session.
func (m *Manager) CloseSession(sessionID string) {
	m.mu.Lock()
	servers := m.by[sessionID]
	delete(m.by, sessionID)
	var sessions []*mcp.ClientSession
	for _, conn := range servers {
		conn.closed = true
		if conn.cancel != nil {
			conn.cancel()
		}
		if conn.session != nil {
			sessions = append(sessions, conn.session)
		}
	}
	m.mu.Unlock()
	for _, cs := range sessions {
		_ = cs.Close()
	}
}

// Close releases all live MCP connections owned by the manager.
func (m *Manager) Close() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.by))
	for id := range m.by {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.CloseSession(id)
	}
}

// CloseExcept leaves active prompt connections untouched; the server queues
// their reload and calls CloseSession after the run releases ownership.
func (m *Manager) CloseExcept(keep map[string]bool) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.by))
	for id := range m.by {
		if !keep[id] {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.CloseSession(id)
	}
}

type sdkTool struct {
	manager    *Manager
	conn       *connection
	sessionID  string
	server     string
	definition ToolDefinition
}

func (t sdkTool) Name() string               { return t.definition.Name }
func (t sdkTool) Description() string        { return t.definition.Description }
func (t sdkTool) Prompt() string             { return t.definition.Description }
func (t sdkTool) Snippet() string            { return t.server + ": " + t.definition.Description }
func (t sdkTool) Parameters() map[string]any { return t.definition.InputSchema }

func (t sdkTool) Validate(args map[string]any) error {
	if msg := loop.SchemaErrors(t.Parameters(), t.definition.Name, args); msg != "" {
		return fmt.Errorf("%w: %s", errMCPTool, msg)
	}
	return nil
}

func (t sdkTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	callName := t.definition.Name
	if t.definition.WireName != "" {
		callName = t.definition.WireName
	}
	result, err := t.conn.session.CallTool(ctx, &mcp.CallToolParams{Name: callName, Arguments: args})
	if err != nil {
		// A transport error does not prove the server skipped execution. Retrying
		// here can duplicate non-idempotent side effects, so reconnect next prompt.
		t.manager.drop(t.sessionID, t.server, t.conn)
		if t.manager.notify != nil {
			t.manager.notify(t.sessionID, Notification{Kind: "server_failed", Server: t.server, Message: err.Error()})
		}
		return errorResult(err)
	}
	return decodeResult(result)
}

func errorResult(err error) loop.ToolResult {
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: err.Error()}}, IsError: true}
}

func decodeResult(result *mcp.CallToolResult) loop.ToolResult {
	out := loop.ToolResult{IsError: result.IsError}
	for _, block := range result.Content {
		switch content := block.(type) {
		case *mcp.TextContent:
			out.Content = append(out.Content, types.Content{Type: "text", Text: content.Text})
		case *mcp.ImageContent:
			out.Content = append(out.Content, types.Content{Type: "image", Data: base64.StdEncoding.EncodeToString(content.Data), MIMEType: content.MIMEType})
		case *mcp.AudioContent:
			out.Content = append(out.Content, types.Content{Type: "audio", Data: base64.StdEncoding.EncodeToString(content.Data), MIMEType: content.MIMEType})
		default:
			raw, _ := block.MarshalJSON()
			out.Content = append(out.Content, types.Content{Type: "text", Text: string(raw)})
		}
	}
	if len(out.Content) == 0 && result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return errorResult(fmt.Errorf("marshal MCP structured content: %w", err))
		}
		out.Content = append(out.Content, types.Content{Type: "text", Text: string(raw)})
	}
	if len(out.Content) == 0 {
		out.Content = []types.Content{{Type: "text", Text: "MCP tool returned no content"}}
	}
	return out
}
