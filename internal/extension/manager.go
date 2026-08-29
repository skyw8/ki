package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/types"
)

// Manager owns process-global sidecar processes and session-scoped views of
// their registrations. Session IDs are carried on every RPC that can touch
// session state, so one sidecar can safely serve multiple sessions.
type Manager struct {
	mu            sync.Mutex
	startMu       sync.Mutex
	startLocks    map[string]*sync.Mutex
	by            map[string]*rpcClient
	order         map[string][]string
	sessionTools  map[string]map[string][]ToolSpec
	sessionOpen   map[string]map[string]bool
	descs         map[string]Descriptor
	status        map[string]RuntimeStatus
	watching      map[string]bool
	runtimeCtx    context.Context
	runtimeCancel context.CancelFunc
	providerAuth  func(ProviderAuthEvent)
	onErr         ErrorFunc
	home          string
	host          SessionHost
}

// RuntimeStatus describes one server-level extension runtime.
type RuntimeStatus struct {
	Name         string   `json:"name"`
	State        string   `json:"state"`
	Error        string   `json:"error,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
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
		startLocks:   map[string]*sync.Mutex{},
		order:        map[string][]string{},
		sessionTools: map[string]map[string][]ToolSpec{},
		sessionOpen:  map[string]map[string]bool{},
		descs:        map[string]Descriptor{},
		status:       map[string]RuntimeStatus{},
		watching:     map[string]bool{},
		onErr:        onErr,
		home:         home,
	}
}

// SetHost attaches inbound Host methods (session enqueue, bus, UI).
func (m *Manager) SetHost(h SessionHost) { m.host = h }

// SetProviderAuthHandler forwards private provider-auth notifications from
// the shared server-level sidecars to the provider HTTP/UI owner.
func (m *Manager) SetProviderAuthHandler(fn func(ProviderAuthEvent)) {
	m.mu.Lock()
	m.providerAuth = fn
	clients := make([]*rpcClient, 0, len(m.by))
	for _, c := range m.by {
		clients = append(clients, c)
	}
	m.mu.Unlock()
	for _, c := range clients {
		c.setProviderAuthHandler(fn)
	}
}

// Configure reconciles the process-global sidecars with the latest global
// catalog. Disabled, removed, or changed packages are no longer available to
// new session views and their old processes are closed.
func (m *Manager) Configure(descriptors []Descriptor) {
	desired := map[string]Descriptor{}
	for _, d := range descriptors {
		if d.Enabled && d.Error == "" && d.wantsSidecar() {
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
		for sessionID, opened := range m.sessionOpen {
			delete(opened, name)
			if len(opened) == 0 {
				delete(m.sessionOpen, sessionID)
			}
		}
	}
	m.descs = desired
	for name := range m.status {
		if _, ok := desired[name]; !ok {
			delete(m.status, name)
		}
	}
	m.mu.Unlock()
	for _, c := range closing {
		c.close()
	}
}

// Start launches every enabled executable extension after the server has
// begun listening. It is intentionally independent of session creation.
// A failed sidecar is recorded and retried while its descriptor remains
// enabled; one extension never blocks the others.
func (m *Manager) Start(ctx context.Context, descriptors []Descriptor) {
	m.mu.Lock()
	if m.runtimeCancel == nil {
		m.runtimeCtx, m.runtimeCancel = context.WithCancel(ctx)
	}
	runtimeCtx := m.runtimeCtx
	m.mu.Unlock()
	m.Configure(descriptors)
	for _, d := range descriptors {
		if !d.wantsSidecar() {
			continue
		}
		m.mu.Lock()
		if m.watching[d.Name] {
			m.mu.Unlock()
			continue
		}
		m.watching[d.Name] = true
		m.status[d.Name] = RuntimeStatus{Name: d.Name, State: "starting", Capabilities: append([]string(nil), d.Capabilities...)}
		m.mu.Unlock()
		//nolint:contextcheck // sidecars share process runtimeCtx across Start calls
		go m.watchRuntime(runtimeCtx, d)
	}
}

// RuntimeStatuses returns a stable snapshot for the settings page and logs.
func (m *Manager) RuntimeStatuses() []RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RuntimeStatus, 0, len(m.status))
	for _, state := range m.status {
		state.Capabilities = append([]string(nil), state.Capabilities...)
		out = append(out, state)
	}
	slices.SortFunc(out, func(a, b RuntimeStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (m *Manager) watchRuntime(ctx context.Context, d Descriptor) {
	defer func() {
		m.mu.Lock()
		delete(m.watching, d.Name)
		m.mu.Unlock()
	}()
	for {
		m.mu.Lock()
		desired, ok := m.descs[d.Name]
		m.mu.Unlock()
		if !ok || ctx.Err() != nil {
			return
		}
		// Reload may replace a manifest while this watcher is waiting for the
		// old process to exit. Re-read the descriptor here so one watcher follows
		// the replacement instead of leaving the new runtime unstarted.
		d = desired
		c := m.ensure(ctx, "", d)
		if c == nil {
			m.setRuntimeStatus(d.Name, "failed", "sidecar failed to start", d.Capabilities)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		m.setRuntimeStatus(d.Name, "ready", "", d.Capabilities)
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
		}
		m.mu.Lock()
		if current := m.by[d.Name]; current == c {
			delete(m.by, d.Name)
			for sessionID, opened := range m.sessionOpen {
				delete(opened, d.Name)
				if len(opened) == 0 {
					delete(m.sessionOpen, sessionID)
				}
			}
		}
		m.mu.Unlock()
		c.close()
		m.setRuntimeStatus(d.Name, "restarting", "sidecar exited", d.Capabilities)
	}
}

func (m *Manager) setRuntimeStatus(name, state, message string, caps []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status[name] = RuntimeStatus{Name: name, State: state, Error: message, Capabilities: append([]string(nil), caps...)}
}

func sameRuntime(a, b Descriptor) bool {
	return a.Name == b.Name && a.Version == b.Version && a.Path == b.Path &&
		a.FailClosed == b.FailClosed && reflect.DeepEqual(a.Capabilities, b.Capabilities) &&
		reflect.DeepEqual(a.manifest.Runtime, b.manifest.Runtime)
}

// Prepare opens this session's view over already-running server sidecars.
func (m *Manager) Prepare(ctx context.Context, sessionID, cwd string, enabled []Descriptor) []loop.Tool {
	var tools []loop.Tool
	var order []string
	var opened []*rpcClient
	for _, d := range enabled {
		if !d.wantsSidecar() {
			continue
		}
		order = append(order, d.Name)
		c := m.ensure(ctx, sessionID, d)
		if c == nil {
			continue
		}
		if m.markSessionOpen(sessionID, d.Name) {
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
	for _, c := range opened {
		c.notify("session.open", map[string]any{"sessionId": sessionID, "cwd": cwd})
	}
	return tools
}

// markSessionOpen records the session view per sidecar. A runtime can fail
// during the first prepare and become ready later; keeping this state per
// extension lets the next prepare deliver session.open exactly once.
func (m *Manager) markSessionOpen(sessionID, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	opened := m.sessionOpen[sessionID]
	if opened == nil {
		opened = map[string]bool{}
		m.sessionOpen[sessionID] = opened
	}
	if opened[name] {
		return false
	}
	opened[name] = true
	return true
}

func (m *Manager) ensure(ctx context.Context, sessionID string, d Descriptor) *rpcClient {
	m.mu.Lock()
	if c := m.by[d.Name]; c != nil {
		select {
		case <-c.closed:
			delete(m.by, d.Name)
		default:
			m.mu.Unlock()
			return c
		}
	}
	m.mu.Unlock()
	// Only one goroutine may launch a given global sidecar at a time. The
	// second lookup after acquiring the lock avoids duplicate processes when
	// two sessions prepare the same extension concurrently.
	startLock := m.startLock(d.Name)
	startLock.Lock()
	defer startLock.Unlock()
	m.mu.Lock()
	if c := m.by[d.Name]; c != nil {
		select {
		case <-c.closed:
			delete(m.by, d.Name)
		default:
			m.mu.Unlock()
			return c
		}
	}
	host := m.host
	home := m.home
	m.mu.Unlock()
	c, err := startRPC(ctx, d, "", home, "", host)
	if err != nil {
		if sessionID != "" {
			m.reportError(sessionID, d.Name, "runtime", "sidecar_start", err.Error())
		}
		slog.Info("extension sidecar failed", "extension", d.Name, "err", err)
		return nil
	}
	if ctx.Err() != nil {
		c.close()
		return nil
	}
	m.mu.Lock()
	providerAuth := m.providerAuth
	m.mu.Unlock()
	c.setProviderAuthHandler(providerAuth)
	m.mu.Lock()
	if existing := m.by[d.Name]; existing != nil {
		m.mu.Unlock()
		c.close()
		return existing
	}
	m.by[d.Name] = c
	m.mu.Unlock()
	for _, cap := range c.undeclared {
		m.reportError(sessionID, d.Name, cap, "undeclared", "initialize returned "+cap+" without capability")
	}
	return c
}

func (m *Manager) startLock(name string) *sync.Mutex {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if lock := m.startLocks[name]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	m.startLocks[name] = lock
	return lock
}

// globalClient returns a shared server-level sidecar for provider requests.
// The fallback launch keeps direct in-process users working before ListenAndServe;
// the normal server path has already started every runtime after binding.
func (m *Manager) globalClient(ctx context.Context, name string) (*rpcClient, error) {
	m.mu.Lock()
	c := m.by[name]
	d, ok := m.descs[name]
	m.mu.Unlock()
	if c != nil {
		select {
		case <-c.closed:
		default:
			return c, nil
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w: %q", errRuntimeNotRegistered, name)
	}
	if c = m.ensure(ctx, "", d); c == nil {
		return nil, fmt.Errorf("%w: %q", errRuntimeUnavailable, name)
	}
	return c, nil
}

func (m *Manager) reportError(sessionID, name, capability, code, message string) {
	if m.onErr != nil {
		m.onErr(sessionID, name, capability, code, message)
	}
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
	return func(name, capability, code, message string) {
		m.reportError(sessionID, name, capability, code, message)
	}
}

// Occupy binds this session's interceptors to one occupy-wide skip set.
func (m *Manager) Occupy(sessionID string) *Occupy {
	return &Occupy{items: m.items(sessionID), skipped: newSkipSet(), onErr: m.errFn(sessionID)}
}

// Hooks returns occupy-scoped loop hooks for this session's interceptors.
func (o *Occupy) Hooks() loop.Hooks {
	if o == nil {
		return loop.Hooks{}
	}
	return composeHooks(o.items, o.skipped, o.onErr)
}

// WrapStreamer wraps the live occupy streamer with provider lifecycle hooks.
func (o *Occupy) WrapStreamer(inner loop.Streamer) loop.Streamer {
	if o == nil {
		return inner
	}
	return wrapStreamer(inner, o.items, o.skipped, o.onErr)
}

// HTTPDoer returns an HTTP round-tripper that applies before_provider_headers.
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

// OnEvent fans out a redacted event to async subscribers.
func (m *Manager) OnEvent(ctx context.Context, sessionID string, ev Event) {
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
	ctx = withSessionID(ctx, sessionID)
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
	runtimeCancel := m.runtimeCancel
	m.runtimeCtx = nil
	m.runtimeCancel = nil
	clients := make([]*rpcClient, 0, len(m.by))
	for _, c := range m.by {
		clients = append(clients, c)
	}
	m.by = map[string]*rpcClient{}
	m.order = map[string][]string{}
	m.sessionTools = map[string]map[string][]ToolSpec{}
	m.sessionOpen = map[string]map[string]bool{}
	m.descs = map[string]Descriptor{}
	m.status = map[string]RuntimeStatus{}
	m.watching = map[string]bool{}
	m.mu.Unlock()
	if runtimeCancel != nil {
		runtimeCancel()
	}
	for _, c := range clients {
		c.close()
	}
}

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

// NotifyGlobal sends a process-level notification to one running sidecar.
// Extensions that support configuration should reload their private config
// file after receiving config.updated; a stopped sidecar reads it on startup.
func (m *Manager) NotifyGlobal(name, method string, params any) {
	m.mu.Lock()
	c := m.by[name]
	m.mu.Unlock()
	if c != nil {
		c.notify(method, params)
	}
}

func (m *Manager) client(sessionID, name string) *rpcClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.order[sessionID], name) {
		return m.by[name]
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

// Enabled filters snapshot.Extensions (Discover.All) with the live toggle and
// returns Discover chain order (global-by-name). Server Prepare must use this
// (or Discover.Enabled), never a bare name-sorted All.
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
