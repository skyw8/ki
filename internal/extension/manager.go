package extension

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/types"
)

// Manager owns session-scoped sidecar processes.
type Manager struct {
	mu    sync.Mutex
	by    map[string]map[string]*rpcClient
	order map[string][]string
	onErr ErrorFunc
	home  string
	host  SessionHost
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
		by:    map[string]map[string]*rpcClient{},
		order: map[string][]string{},
		onErr: onErr,
		home:  home,
	}
}

// SetHost attaches inbound Host methods (session enqueue, bus, UI).
func (m *Manager) SetHost(h SessionHost) { m.host = h }

// Prepare starts sidecars for enabled packages that want runtime.
func (m *Manager) Prepare(ctx context.Context, sessionID, cwd string, enabled []Descriptor) []loop.Tool {
	var tools []loop.Tool
	var order []string
	for _, d := range enabled {
		if !d.wantsSidecar() {
			continue
		}
		// Store Discover order (global-by-name then project-by-name) so
		// items() does not range the session map.
		order = append(order, d.Name)
		c := m.ensure(ctx, sessionID, cwd, d)
		if c == nil {
			continue
		}
		tools = append(tools, toolsFromRegistration(c)...)
	}
	m.mu.Lock()
	m.order[sessionID] = order
	m.mu.Unlock()
	return tools
}

func (m *Manager) ensure(ctx context.Context, sessionID, cwd string, d Descriptor) *rpcClient {
	m.mu.Lock()
	if m.by[sessionID] != nil {
		if c := m.by[sessionID][d.Name]; c != nil {
			m.mu.Unlock()
			return c
		}
	}
	m.mu.Unlock()
	c, err := startRPC(ctx, d, sessionID, m.home, cwd, m.host)
	if err != nil {
		if m.onErr != nil {
			m.onErr(sessionID, d.Name, "runtime", "sidecar_start", err.Error())
		}
		slog.Info("extension sidecar failed", "extension", d.Name, "err", err)
		return nil
	}
	m.mu.Lock()
	if m.by[sessionID] == nil {
		m.by[sessionID] = map[string]*rpcClient{}
	}
	if existing := m.by[sessionID][d.Name]; existing != nil {
		m.mu.Unlock()
		c.close()
		return existing
	}
	m.by[sessionID][d.Name] = c
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
	clients := m.by[sessionID]
	order := m.order[sessionID]
	seen := map[string]bool{}
	var out []namedInterceptor
	appendOne := func(name string) {
		c := clients[name]
		if c == nil || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, namedInterceptor{
			name: name, failClosed: c.failClosed, syncEvents: c.registration.syncEvents, inner: c,
		})
	}
	for _, name := range order {
		appendOne(name)
	}
	var extra []string
	for name := range clients {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
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

// RuntimeCommands lists executable slash handlers from running sidecars.
func (m *Manager) RuntimeCommands(sessionID string) map[string]CommandSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]CommandSpec{}
	for _, c := range m.by[sessionID] {
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
	for _, c := range m.by[sessionID] {
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
	return client.invokeCommand(ctx, name, args)
}

// OnEvent fans out a redacted event asynchronously.
func (m *Manager) OnEvent(sessionID string, ev Event) {
	m.mu.Lock()
	clients := make([]*rpcClient, 0, len(m.by[sessionID]))
	for _, c := range m.by[sessionID] {
		if len(c.registration.asyncEvents) > 0 {
			clients = append(clients, c)
		}
	}
	m.mu.Unlock()
	ctx := context.Background()
	for _, c := range clients {
		_ = c.OnEvent(ctx, ev)
	}
}

// CloseSession kills sidecars for one session.
func (m *Manager) CloseSession(sessionID string) {
	m.mu.Lock()
	clients := m.by[sessionID]
	delete(m.by, sessionID)
	delete(m.order, sessionID)
	m.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

// CloseExcept closes sessions not in active.
func (m *Manager) CloseExcept(active map[string]bool) {
	m.mu.Lock()
	var ids []string
	for id := range m.by {
		if !active[id] {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.CloseSession(id)
	}
}

// Enabled filters snapshot.Extensions (Discover.All) with the live toggle and
// returns Discover chain order (global-by-name, then project-by-name). Server
// Prepare must use this (or Discover.Enabled), never a bare name-sorted All.
// ApplyInput runs sync input subscribers. swallow true means drop the prompt.
func (m *Manager) ApplyInput(ctx context.Context, sessionID, text string) (string, bool) {
	for _, it := range m.items(sessionID) {
		if !it.hasSync(EventInput) {
			continue
		}
		c, ok := it.inner.(*rpcClient)
		if !ok {
			continue
		}
		next, swallow, err := c.rewriteInput(ctx, text)
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
		c, ok := it.inner.(*rpcClient)
		if !ok {
			continue
		}
		next, err := c.rewriteMessageEnd(ctx, msg)
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
		c, ok := it.inner.(*rpcClient)
		if !ok {
			continue
		}
		okc, summary, err := c.beforeCompact(ctx)
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

// RegisterTools appends tool specs for the next occupy Prepare.
func (m *Manager) RegisterTools(sessionID, name string, tools []ToolSpec) error {
	c := m.client(sessionID, name)
	if c == nil {
		return errRPC
	}
	c.registration.Tools = append(c.registration.Tools, tools...)
	return nil
}

// Notify sends a Host→sidecar JSON-RPC notification (ui.action / ui.submit).
func (m *Manager) Notify(sessionID, name, method string, params any) {
	c := m.client(sessionID, name)
	if c == nil {
		return
	}
	c.notify(method, params)
}

func (m *Manager) client(sessionID, name string) *rpcClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.by[sessionID] == nil {
		return nil
	}
	return m.by[sessionID][name]
}

// BusEmit deep-copies data and fans out to other bus subscribers, returning merged data.
func (m *Manager) BusEmit(sessionID, from, channel string, data any) (any, error) {
	cloned := cloneJSON(data)
	m.mu.Lock()
	var others []*rpcClient
	for name, c := range m.by[sessionID] {
		if name == from {
			continue
		}
		others = append(others, c)
	}
	m.mu.Unlock()
	for _, c := range others {
		cloned = c.deliverBus(channel, cloned, true)
	}
	return cloned, nil
}

// BusBroadcast fire-and-forget to other subscribers.
func (m *Manager) BusBroadcast(sessionID, from, channel string, data any) error {
	cloned := cloneJSON(data)
	m.mu.Lock()
	var others []*rpcClient
	for name, c := range m.by[sessionID] {
		if name == from {
			continue
		}
		others = append(others, c)
	}
	m.mu.Unlock()
	for _, c := range others {
		c.deliverBus(channel, cloned, false)
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
