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

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/types"
)

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
	watching      map[string]chan struct{}
	configured    bool
	closed        bool
	known         map[string]bool
	adopted       map[string]Descriptor
	generation    uint64
	changed       chan struct{}
	runtimeCtx    context.Context
	runtimeCancel context.CancelFunc
	providerAuth  func(ProviderAuthEvent)
	onErr         ErrorFunc
	onStatus      func(RuntimeStatus)
	home          string
	host          SessionHost
}

type RuntimeStatus struct {
	Name         string   `json:"name"`
	State        string   `json:"state"`
	Error        string   `json:"error,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

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

type Occupy struct {
	items   []namedInterceptor
	skipped *skipSet
	onErr   func(name, capability, code, message string)
}

func (o *Occupy) OpaqueReplaySafe() bool {
	if o == nil {
		return true
	}
	for _, item := range o.items {
		for _, event := range []string{
			EventBeforeAgentStart,
			EventContext,
			EventBeforeProviderRequest,
			EventBeforeProviderHeaders,
			EventMessageEnd,
		} {
			if item.hasSync(event) {
				return false
			}
		}
	}
	return true
}

func NewManager(home string, onErr ErrorFunc) *Manager {
	return &Manager{
		by:           map[string]*rpcClient{},
		startLocks:   map[string]*sync.Mutex{},
		order:        map[string][]string{},
		sessionTools: map[string]map[string][]ToolSpec{},
		sessionOpen:  map[string]map[string]bool{},
		descs:        map[string]Descriptor{},
		status:       map[string]RuntimeStatus{},
		watching:     map[string]chan struct{}{},
		known:        map[string]bool{},
		adopted:      map[string]Descriptor{},
		changed:      make(chan struct{}),
		onErr:        onErr,
		home:         home,
	}
}

func (m *Manager) SetHost(h SessionHost) { m.host = h }

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

func (m *Manager) SetStatusHandler(fn func(RuntimeStatus)) {
	m.mu.Lock()
	m.onStatus = fn
	m.mu.Unlock()
}

func (m *Manager) Configure(descriptors []Descriptor) {
	desired := map[string]Descriptor{}
	for _, d := range descriptors {
		if d.Enabled && d.Error == "" && d.wantsSidecar() {
			desired[d.Name] = d
		}
	}
	m.mu.Lock()
	if !m.configured || len(m.adopted) > 0 || !sameRuntimes(m.descs, desired) {
		m.generation++
		close(m.changed)
		m.changed = make(chan struct{})
	}
	m.configured = true
	for _, d := range descriptors {
		m.known[d.Name] = true
	}
	m.adopted = map[string]Descriptor{}
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

func (m *Manager) Start(ctx context.Context, descriptors []Descriptor) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
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
		if m.watching[d.Name] != nil {
			m.mu.Unlock()
			continue
		}
		done := make(chan struct{})
		m.watching[d.Name] = done
		m.status[d.Name] = RuntimeStatus{Name: d.Name, State: "starting", Capabilities: slices.Clone(d.Capabilities)}
		m.mu.Unlock()
		//nolint:contextcheck // sidecars share process runtimeCtx across Start calls
		go m.watchRuntime(runtimeCtx, d, done, defaultRuntimeHooks(m))
	}
}

func (m *Manager) RuntimeStatuses() []RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RuntimeStatus, 0, len(m.status))
	for _, state := range m.status {
		state.Capabilities = slices.Clone(state.Capabilities)
		out = append(out, state)
	}
	slices.SortFunc(out, func(a, b RuntimeStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (m *Manager) setRuntimeStatus(d Descriptor, state, message string) {
	name := d.Name
	st := RuntimeStatus{Name: name, State: state, Error: message, Capabilities: slices.Clone(d.Capabilities)}
	m.mu.Lock()
	if desired, ok := m.descs[name]; !ok || !sameRuntime(desired, d) {
		m.mu.Unlock()
		return
	}
	prev, seen := m.status[name]
	m.status[name] = st
	notify := m.onStatus
	m.mu.Unlock()
	if notify != nil && (!seen || !sameStatus(prev, st)) {
		notify(st)
	}
}

func sameStatus(a, b RuntimeStatus) bool {
	return a.Name == b.Name && a.State == b.State && a.Error == b.Error &&
		slices.Equal(a.Capabilities, b.Capabilities)
}

func sameRuntime(a, b Descriptor) bool {
	return a.Name == b.Name && a.Version == b.Version && a.Path == b.Path &&
		a.FailClosed == b.FailClosed && reflect.DeepEqual(a.Capabilities, b.Capabilities) &&
		reflect.DeepEqual(a.manifest.Runtime, b.manifest.Runtime)
}

func (m *Manager) Prepare(ctx context.Context, sessionID, cwd string, enabled []Descriptor) []toolapi.Tool {
	var tools []toolapi.Tool
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
		dynamic := slices.Clone(m.sessionTools[sessionID][d.Name])
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
	m.adoptSnapshotLocked(d)
	if !m.desiredLocked(d) {
		m.mu.Unlock()
		return nil
	}
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
	startLock := m.startLock(d.Name)
	startLock.Lock()
	defer startLock.Unlock()
	m.mu.Lock()
	if !m.desiredLocked(d) {
		m.mu.Unlock()
		return nil
	}
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
	generation := m.generation
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
	return m.publishRuntime(ctx, sessionID, d, generation, c)
}

func (m *Manager) publishRuntime(ctx context.Context, sessionID string, d Descriptor, generation uint64, c *rpcClient) *rpcClient {
	m.mu.Lock()
	providerAuth := m.providerAuth
	m.mu.Unlock()
	c.setProviderAuthHandler(providerAuth)
	m.mu.Lock()
	if ctx.Err() != nil || generation != m.generation || !m.desiredLocked(d) {
		m.mu.Unlock()
		c.close()
		return nil
	}
	if existing := m.by[d.Name]; existing != nil {
		m.mu.Unlock()
		c.close()
		return existing
	}
	combined := append([]ToolSpec(nil), c.registration.Tools...)
	for _, existing := range m.by {
		combined = append(combined, existing.registration.Tools...)
	}
	if err := validateToolNames(combined); err != nil {
		m.mu.Unlock()
		c.close()
		m.reportError(sessionID, d.Name, "tools", "tool_name_collision", err.Error())
		return nil
	}
	for _, byExtension := range m.sessionTools {
		scoped := append([]ToolSpec(nil), combined...)
		for _, dynamic := range byExtension {
			scoped = append(scoped, dynamic...)
		}
		if err := validateToolNames(scoped); err != nil {
			m.mu.Unlock()
			c.close()
			m.reportError(sessionID, d.Name, "tools", "tool_name_collision", err.Error())
			return nil
		}
	}
	m.by[d.Name] = c
	m.known[d.Name] = true
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

func (o *Occupy) TransformProviderRequest(ctx context.Context, req loop.Request) (loop.Request, error) {
	if o == nil {
		return req, nil
	}
	live, short, err := applyBeforeProvider(ctx, req, o.items, o.skipped, o.onErr)
	if err != nil {
		return loop.Request{}, err
	}
	if short != "" {
		return loop.Request{}, fmt.Errorf("provider request short-circuited during compaction")
	}
	return live, nil
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

func (m *Manager) Hooks(sessionID string) loop.Hooks {
	return m.Occupy(sessionID).Hooks()
}

func (m *Manager) WrapStreamer(sessionID string, inner loop.Streamer) loop.Streamer {
	return m.Occupy(sessionID).WrapStreamer(inner)
}

func (m *Manager) HTTPDoer(sessionID string) provider.HTTPDoer {
	items := m.items(sessionID)
	for _, it := range items {
		if it.hasSync(EventBeforeProviderHeaders) {
			return wrapHTTPDoer(nil, items, nil, m.errFn(sessionID))
		}
	}
	return nil
}

func (m *Manager) SessionContributions(sessionID string) map[string]SessionContribution {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := slices.Clone(m.order[sessionID])
	if len(names) == 0 {
		for name := range m.by {
			names = append(names, name)
		}
		slices.Sort(names)
	}
	out := map[string]SessionContribution{}
	for _, name := range names {
		c := m.by[name]
		if c == nil {
			continue
		}
		contrib := SessionContribution{}
		if hasKind(c.capabilities, CapTool) {
			seen := map[string]bool{}
			addTool := func(spec ToolSpec) {
				if spec.Name == "" || catalog.IsReserved(spec.Name) || seen[spec.Name] {
					return
				}
				seen[spec.Name] = true
				aliases, _ := toolapi.Aliases(spec.Name)
				contrib.Tools = append(contrib.Tools, CatalogEntry{Name: toolapi.MustCanonical(spec.Name), Description: spec.Description, Aliases: aliases})
			}
			for _, spec := range c.registration.Tools {
				addTool(spec)
			}
			if sessionID != "" {
				for _, spec := range m.sessionTools[sessionID][name] {
					addTool(spec)
				}
			}
		}
		if hasKind(c.capabilities, CapCommand) {
			for _, spec := range c.registration.Commands {
				if spec.Name == "" || spec.Name == "compact" || spec.Name == "reload" {
					continue
				}
				contrib.Commands = append(contrib.Commands, CatalogEntry{Name: spec.Name, Description: spec.Description})
			}
		}
		if len(contrib.Tools) == 0 && len(contrib.Commands) == 0 {
			continue
		}
		out[name] = contrib
	}
	return out
}

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
	m.watching = map[string]chan struct{}{}
	m.configured = true
	m.closed = true
	m.adopted = map[string]Descriptor{}
	m.generation++
	close(m.changed)
	m.changed = make(chan struct{})
	m.mu.Unlock()
	if runtimeCancel != nil {
		runtimeCancel()
	}
	for _, c := range clients {
		c.close()
	}
}

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

func (m *Manager) BeforeCompact(ctx context.Context, sessionID string, req BeforeCompactRequest) BeforeCompactDecision {
	decision := BeforeCompactDecision{}
	for _, it := range m.items(sessionID) {
		si, ok := it.inner.(sessionInterceptor)
		if !ok {
			continue
		}
		if it.hasSync(EventSessionBeforeCompact) && !decision.Cancel && decision.Result == nil {
			next, err := si.client.beforeCompact(withSessionID(ctx, sessionID), req)
			if err == nil && (next.Cancel || next.Result != nil) {
				decision = next
			}
		}
		si.client.notifyBeforeCompact(withSessionID(ctx, sessionID), req)
	}
	return decision
}

func (m *Manager) RegisterTools(sessionID, name string, tools []ToolSpec) error {
	c := m.client(sessionID, name)
	if c == nil {
		return errRPC
	}
	m.mu.Lock()
	combined := append([]ToolSpec{}, c.registration.Tools...)
	combined = append(combined, m.sessionTools[sessionID][name]...)
	combined = append(combined, tools...)
	for other, client := range m.by {
		if other != name {
			combined = append(combined, client.registration.Tools...)
			combined = append(combined, m.sessionTools[sessionID][other]...)
		}
	}
	if err := validateToolNames(combined); err != nil {
		m.mu.Unlock()
		return err
	}

	if m.sessionTools[sessionID] == nil {
		m.sessionTools[sessionID] = map[string][]ToolSpec{}
	}
	m.sessionTools[sessionID][name] = append(m.sessionTools[sessionID][name], tools...)
	m.mu.Unlock()
	return nil
}

func (m *Manager) Notify(sessionID, name, method string, params any) {
	c := m.client(sessionID, name)
	if c == nil {
		return
	}
	c.notify(method, withSessionParam(sessionID, params))
}

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
