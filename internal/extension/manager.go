package extension

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sync"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/types"
)

// Manager owns process-global sidecar processes and session-scoped views of
// their registrations. Session IDs are carried on every RPC that can touch
// session state, so one sidecar can safely serve multiple sessions.
type Manager struct {
	mu           sync.Mutex
	startMu      sync.Mutex
	by           map[string]*rpcClient
	order        map[string][]string
	sessionTools map[string]map[string][]ToolSpec
	sessionOpen  map[string]bool
	descs        map[string]Descriptor
	onErr        ErrorFunc
	home         string
	host         SessionHost
}

// sessionInterceptor binds a global RPC client to one session for all
// outbound lifecycle calls. The client itself remains shared.
type sessionInterceptor struct {
	client    *rpcClient
	sessionID string
}

func (s sessionInterceptor) ctx(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return withSessionID(ctx, s.sessionID)
}

func (s sessionInterceptor) BeforeRun(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
	return s.client.BeforeRun(s.ctx(ctx), system, msgs)
}
func (s sessionInterceptor) TransformContext(ctx context.Context, msgs []types.Message) ([]types.Message, error) {
	return s.client.TransformContext(s.ctx(ctx), msgs)
}
func (s sessionInterceptor) BeforeTool(ctx context.Context, in ToolCall) (ToolCall, *Block, error) {
	return s.client.BeforeTool(s.ctx(ctx), in)
}
func (s sessionInterceptor) AfterTool(ctx context.Context, in ToolCall, res ResultPatch) (ResultPatch, error) {
	return s.client.AfterTool(s.ctx(ctx), in, res)
}
func (s sessionInterceptor) BeforeProvider(ctx context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error) {
	return s.client.BeforeProvider(s.ctx(ctx), req)
}
func (s sessionInterceptor) BeforeProviderHTTP(ctx context.Context, view HTTPRequestView) (HTTPRequestPatch, error) {
	return s.client.BeforeProviderHTTP(s.ctx(ctx), view)
}
func (s sessionInterceptor) AfterProviderHTTP(ctx context.Context, status int, headers map[string]string) error {
	return s.client.AfterProviderHTTP(s.ctx(ctx), status, headers)
}
func (s sessionInterceptor) AfterProviderError(ctx context.Context, errClass string) (Fallback, error) {
	return s.client.AfterProviderError(s.ctx(ctx), errClass)
}
func (s sessionInterceptor) OnEvent(ctx context.Context, ev Event) error {
	ev.SessionID = s.sessionID
	return s.client.OnEvent(s.ctx(ctx), ev)
}

// Occupy is one prompt occupy: lifecycle chain plus the skip set shared by
// the occupy Streamer and the session HTTPDoer for that run.
type Occupy struct {
	items   []namedInterceptor
	skipped *skipSet
	onErr   func(name, capability, code, message string)
}

// NewManager creates a manager. onErr may be nil.
func NewManager(home string, onErr ErrorFunc) *Manager {
	return &Manager{
		by:           map[string]*rpcClient{},
		order:        map[string][]string{},
		sessionTools: map[string]map[string][]ToolSpec{},
		sessionOpen:  map[string]bool{},
		descs:        map[string]Descriptor{},
		onErr:        onErr,
		home:         home,
	}
}

// SetHost attaches inbound Host methods (session enqueue, bus, UI).
func (m *Manager) SetHost(h SessionHost) { m.host = h }

// Configure reconciles the process-global sidecars with the latest global
// catalog. Disabled, removed, or changed packages are no longer available to
// new session views and their old processes are closed.
func (m *Manager) Configure(descriptors []Descriptor) {
	desired := map[string]Descriptor{}
	for _, d := range descriptors {
		if d.Enabled && d.Error == "" && d.wantsSessionSidecar() {
			desired[d.Name] = d
		}
	}
	m.mu.Lock()
	var closing []*rpcClient
	for name, c := range m.by {
		d, ok := desired[name]
		if ok && sameRuntime(m.descs[name], d) {
			continue
		}
		closing = append(closing, c)
		delete(m.by, name)
		for sessionID, tools := range m.sessionTools {
			delete(tools, name)
			if len(tools) == 0 {
				delete(m.sessionTools, sessionID)
			}
		}
	}
	m.descs = desired
	m.mu.Unlock()
	for _, c := range closing {
		c.close()
	}
}

func sameRuntime(a, b Descriptor) bool {
	return a.Name == b.Name && a.Version == b.Version && a.Path == b.Path &&
		a.FailClosed == b.FailClosed && reflect.DeepEqual(a.Capabilities, b.Capabilities) &&
		reflect.DeepEqual(a.manifest.Runtime, b.manifest.Runtime)
}

// Prepare starts sidecars for enabled packages that want runtime.
func (m *Manager) Prepare(ctx context.Context, sessionID, cwd string, enabled []Descriptor) []loop.Tool {
	var tools []loop.Tool
	var order []string
	m.mu.Lock()
	firstPrepare := !m.sessionOpen[sessionID]
	m.sessionOpen[sessionID] = true
	m.mu.Unlock()
	var opened []*rpcClient
	for _, d := range enabled {
		if !d.wantsSessionSidecar() {
			continue
		}
		order = append(order, d.Name)
		c := m.ensure(ctx, sessionID, d)
		if c == nil {
			continue
		}
		if firstPrepare {
			opened = append(opened, c)
		}
		tools = append(tools, toolsFromRegistration(c, sessionID)...)
		m.mu.Lock()
		dynamic := append([]ToolSpec(nil), m.sessionTools[sessionID][d.Name]...)
		m.mu.Unlock()
		tools = append(tools, toolsFromSpecs(c, sessionID, dynamic)...)
	}
	m.mu.Lock()
	m.order[sessionID] = order
	m.mu.Unlock()
	if firstPrepare {
		for _, c := range opened {
			c.notify("session.open", map[string]any{"sessionId": sessionID, "cwd": cwd})
		}
	}
	return tools
}

func (m *Manager) ensure(ctx context.Context, sessionID string, d Descriptor) *rpcClient {
	m.mu.Lock()
	if c := m.by[d.Name]; c != nil {
		m.mu.Unlock()
		return c
	}
	m.mu.Unlock()
	// Only one goroutine may launch a given global sidecar at a time. The
	// second lookup after acquiring the lock avoids duplicate processes when
	// two sessions prepare the same extension concurrently.
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	if c := m.by[d.Name]; c != nil {
		m.mu.Unlock()
		return c
	}
	host := m.host
	home := m.home
	m.mu.Unlock()
	c, err := startRPC(ctx, d, "", home, "", host)
	if err != nil {
		if m.onErr != nil {
			m.onErr(sessionID, d.Name, "runtime", "sidecar_start", err.Error())
		}
		slog.Info("extension sidecar failed", "extension", d.Name, "err", err)
		return nil
	}
	m.mu.Lock()
	if existing := m.by[d.Name]; existing != nil {
		m.mu.Unlock()
		c.close()
		return existing
	}
	m.by[d.Name] = c
	m.mu.Unlock()
	for _, cap := range c.undeclared {
		if m.onErr != nil {
			m.onErr(sessionID, d.Name, cap, "undeclared", "initialize returned "+cap+" without capability")
		}
	}
	return c
}

func (m *Manager) items(sessionID string) []namedInterceptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	order := m.order[sessionID]
	var out []namedInterceptor
	appendOne := func(name string) {
		c := m.by[name]
		if c == nil {
			return
		}
		out = append(out, namedInterceptor{
			name: name, failClosed: c.failClosed, syncEvents: c.registration.syncEvents,
			inner: sessionInterceptor{client: c, sessionID: sessionID},
		})
	}
	for _, name := range order {
		appendOne(name)
	}
	return out
}

func (m *Manager) errFn(sessionID string) func(name, capability, code, message string) {
	return func(name, cap, code, message string) {
		if m.onErr != nil {
			m.onErr(sessionID, name, cap, code, message)
		}
	}
}

// Occupy binds this session's interceptors to one occupy-wide skip set.
func (m *Manager) Occupy(sessionID string) *Occupy {
	return &Occupy{items: m.items(sessionID), skipped: newSkipSet(), onErr: m.errFn(sessionID)}
}

func (o *Occupy) Hooks() loop.Hooks {
	if o == nil {
		return loop.Hooks{}
	}
	return composeHooks(o.items, o.skipped, o.onErr)
}

func (o *Occupy) WrapStreamer(inner loop.Streamer) loop.Streamer {
	if o == nil {
		return inner
	}
	return wrapStreamer(inner, o.items, o.skipped, o.onErr)
}

func (o *Occupy) HTTPDoer() provider.HTTPDoer {
	if o == nil {
		return nil
	}
	for _, it := range o.items {
		if it.hasSync(EventBeforeProviderHeaders) {
			return wrapHTTPDoer(nil, o.items, o.skipped, o.onErr)
		}
	}
	return nil
}

// Hooks returns occupy-scoped loop hooks for sidecars on this session.
func (m *Manager) Hooks(sessionID string) loop.Hooks {
	return m.Occupy(sessionID).Hooks()
}

// WrapStreamer wraps the live occupy streamer with provider lifecycle.
func (m *Manager) WrapStreamer(sessionID string, inner loop.Streamer) loop.Streamer {
	return m.Occupy(sessionID).WrapStreamer(inner)
}

// HTTPDoer returns a headers-only wrapping doer, or nil if unused.
// Compact uses this session-level Doer (no occupy skip). Live occupy uses Occupy.HTTPDoer.
func (m *Manager) HTTPDoer(sessionID string) provider.HTTPDoer {
	items := m.items(sessionID)
	for _, it := range items {
		if it.hasSync(EventBeforeProviderHeaders) {
			return wrapHTTPDoer(nil, items, nil, m.errFn(sessionID))
		}
	}
	return nil
}

// RuntimeCommands lists executable slash handlers from the session's enabled
// view of the global sidecars.
func (m *Manager) RuntimeCommands(sessionID string) map[string]CommandSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]CommandSpec{}
	for _, name := range m.order[sessionID] {
		c := m.by[name]
		if c == nil {
			continue
		}
		if !hasKind(c.capabilities, CapCommand) {
			continue
		}
		for _, spec := range c.registration.Commands {
			if spec.Name == "" || spec.Name == "compact" || spec.Name == "reload" {
				continue
			}
			if _, exists := out[spec.Name]; exists {
				continue
			}
			out[spec.Name] = spec
		}
	}
	return out
}

// InvokeCommand runs a sidecar slash handler.
func (m *Manager) InvokeCommand(ctx context.Context, sessionID, name, args string) (handled bool, notice, prompt string, err error) {
	m.mu.Lock()
	var client *rpcClient
	for _, extensionName := range m.order[sessionID] {
		c := m.by[extensionName]
		if c == nil {
			continue
		}
		for _, spec := range c.registration.Commands {
			if spec.Name == name {
				client = c
				break
			}
		}
		if client != nil {
			break
		}
	}
	m.mu.Unlock()
	if client == nil {
		return false, "", "", errRPC
	}
	return client.invokeCommand(withSessionID(ctx, sessionID), name, args)
}

// OnEvent fans out a redacted event asynchronously.
func (m *Manager) OnEvent(sessionID string, ev Event) {
	m.mu.Lock()
	clients := make([]*rpcClient, 0, len(m.order[sessionID]))
	seen := map[string]bool{}
	for _, name := range m.order[sessionID] {
		if seen[name] {
			continue
		}
		seen[name] = true
		c := m.by[name]
		if c == nil {
			continue
		}
		if len(c.registration.asyncEvents) > 0 {
			clients = append(clients, c)
		}
	}
	m.mu.Unlock()
	ctx := withSessionID(context.Background(), sessionID)
	for _, c := range clients {
		_ = c.OnEvent(ctx, ev)
	}
}

// CloseSession drops only session state. Global sidecars stay alive until
// Close, allowing another session to reuse the same process.
func (m *Manager) CloseSession(sessionID string) {
	m.mu.Lock()
	var clients []*rpcClient
	seen := map[string]bool{}
	for _, name := range m.order[sessionID] {
		if seen[name] {
			continue
		}
		if c := m.by[name]; c != nil {
			clients = append(clients, c)
		}
		seen[name] = true
	}
	delete(m.order, sessionID)
	delete(m.sessionTools, sessionID)
	delete(m.sessionOpen, sessionID)
	m.mu.Unlock()
	for _, c := range clients {
		c.notify("session.close", map[string]any{"sessionId": sessionID})
	}
}

// CloseExcept drops session state for sessions not in active. It does not
// terminate global sidecars.
func (m *Manager) CloseExcept(active map[string]bool) {
	m.mu.Lock()
	var ids []string
	for id := range m.order {
		if !active[id] {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.CloseSession(id)
	}
}

// Close terminates all process-global sidecars.
func (m *Manager) Close() {
	m.mu.Lock()
	clients := make([]*rpcClient, 0, len(m.by))
	for _, c := range m.by {
		clients = append(clients, c)
	}
	m.by = map[string]*rpcClient{}
	m.order = map[string][]string{}
	m.sessionTools = map[string]map[string][]ToolSpec{}
	m.sessionOpen = map[string]bool{}
	m.descs = map[string]Descriptor{}
	m.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

// Enabled filters snapshot.Extensions (Discover.All) with the live toggle and
// returns Discover chain order (global-by-name). Server
// Prepare must use this (or Discover.Enabled), never a bare name-sorted All.
// ApplyInput runs sync input subscribers. swallow true means drop the prompt.
func (m *Manager) ApplyInput(ctx context.Context, sessionID, text string) (string, bool) {
	for _, it := range m.items(sessionID) {
		if !it.hasSync(EventInput) {
			continue
		}
		si, ok := it.inner.(sessionInterceptor)
		if !ok {
			continue
		}
		next, swallow, err := si.client.rewriteInput(withSessionID(ctx, sessionID), text)
		if err != nil {
			continue
		}
		if swallow {
			return "", true
		}
		text = next
	}
	return text, false
}

// ApplyMessageEnd lets sync subscribers replace a same-role message.
func (m *Manager) ApplyMessageEnd(ctx context.Context, sessionID string, msg types.Message) types.Message {
	for _, it := range m.items(sessionID) {
		if !it.hasSync(EventMessageEnd) {
			continue
		}
		si, ok := it.inner.(sessionInterceptor)
		if !ok {
			continue
		}
		next, err := si.client.rewriteMessageEnd(withSessionID(ctx, sessionID), msg)
		if err != nil {
			continue
		}
		if next.Role == msg.Role && next.Role != "" {
			msg = next
		}
	}
	return msg
}

// CompactAllowed runs session_before_compact. false means skip compact.
func (m *Manager) CompactAllowed(ctx context.Context, sessionID string) (bool, string) {
	for _, it := range m.items(sessionID) {
		if !it.hasSync(EventSessionBeforeCompact) {
			continue
		}
		si, ok := it.inner.(sessionInterceptor)
		if !ok {
			continue
		}
		okc, summary, err := si.client.beforeCompact(withSessionID(ctx, sessionID))
		if err != nil {
			continue
		}
		if !okc {
			return false, ""
		}
		if summary != "" {
			return true, summary
		}
	}
	return true, ""
}

// RegisterTools appends tool specs for this session's next occupy Prepare.
func (m *Manager) RegisterTools(sessionID, name string, tools []ToolSpec) error {
	c := m.client(sessionID, name)
	if c == nil {
		return errRPC
	}
	m.mu.Lock()
	if m.sessionTools[sessionID] == nil {
		m.sessionTools[sessionID] = map[string][]ToolSpec{}
	}
	m.sessionTools[sessionID][name] = append(m.sessionTools[sessionID][name], tools...)
	m.mu.Unlock()
	return nil
}

// Notify sends a Host→sidecar JSON-RPC notification (ui.action / ui.submit).
func (m *Manager) Notify(sessionID, name, method string, params any) {
	c := m.client(sessionID, name)
	if c == nil {
		return
	}
	c.notify(method, withSessionParam(sessionID, params))
}

func (m *Manager) client(sessionID, name string) *rpcClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, enabled := range m.order[sessionID] {
		if enabled == name {
			return m.by[name]
		}
	}
	return nil
}

// BusEmit deep-copies data and fans out to other bus subscribers, returning merged data.
func (m *Manager) BusEmit(sessionID, from, channel string, data any) (any, error) {
	cloned := cloneJSON(data)
	m.mu.Lock()
	var others []*rpcClient
	for _, name := range m.order[sessionID] {
		c := m.by[name]
		if c == nil {
			continue
		}
		if name == from {
			continue
		}
		others = append(others, c)
	}
	m.mu.Unlock()
	for _, c := range others {
		cloned = c.deliverBus(sessionID, channel, cloned, true)
	}
	return cloned, nil
}

// BusBroadcast fire-and-forget to other subscribers.
func (m *Manager) BusBroadcast(sessionID, from, channel string, data any) error {
	cloned := cloneJSON(data)
	m.mu.Lock()
	var others []*rpcClient
	for _, name := range m.order[sessionID] {
		c := m.by[name]
		if c == nil {
			continue
		}
		if name == from {
			continue
		}
		others = append(others, c)
	}
	m.mu.Unlock()
	for _, c := range others {
		c.deliverBus(sessionID, channel, cloned, false)
	}
	return nil
}

func cloneJSON(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

func Enabled(all []Descriptor, toggle session.Toggle) []Descriptor {
	var filtered []Descriptor
	for _, d := range all {
		if d.Error != "" || !toggle.Allowed(d.Name) {
			continue
		}
		d.Enabled = true
		filtered = append(filtered, d)
	}
	return chainOrder(filtered)
}
