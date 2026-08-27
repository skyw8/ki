package extension

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"time"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

// ProviderManager owns the process-level sidecars that implement provider
// runtimes. Unlike Manager, it is not keyed by session: one provider process
// can serve concurrent streams from many sessions.
type ProviderManager struct {
	mu            sync.Mutex
	home          string
	clients       map[string]*rpcClient
	owners        map[string]string
	descs         map[string]Descriptor
	specs         map[string]provider.ExtensionProviderSpec
	refreshLocks  map[string]*sync.Mutex
	authHandlerMu sync.RWMutex
	authHandler   func(ProviderAuthEvent)
	onErr         ErrorFunc
	epoch         uint64
	runtime       *Manager
}

// NewProviderManager creates an empty process-level provider runtime manager.
func NewProviderManager(home string) *ProviderManager {
	return &ProviderManager{
		home:         home,
		clients:      map[string]*rpcClient{},
		owners:       map[string]string{},
		descs:        map[string]Descriptor{},
		specs:        map[string]provider.ExtensionProviderSpec{},
		refreshLocks: map[string]*sync.Mutex{},
	}
}

// SetErrorHandler installs the callback for provider sidecar startup errors.
func (m *ProviderManager) SetErrorHandler(fn ErrorFunc) {
	m.mu.Lock()
	m.onErr = fn
	m.mu.Unlock()
}

// SetRuntimeManager makes provider calls reuse the ordinary extension runtime.
// This keeps one extension package at one server-level sidecar process even
// when it declares both provider and channel/lifecycle capabilities.
func (m *ProviderManager) SetRuntimeManager(runtime *Manager) {
	m.mu.Lock()
	m.runtime = runtime
	var descriptors []Descriptor
	seen := map[string]bool{}
	for _, d := range m.descs {
		if !seen[d.Name] {
			descriptors = append(descriptors, d)
			seen[d.Name] = true
		}
	}
	m.mu.Unlock()
	if runtime != nil {
		// New wires the managers before ListenAndServe. Configure the provider
		// descriptors so a direct in-process request can still use the normal
		// shared client; ListenAndServe later reconciles the complete catalog.
		runtime.Configure(descriptors)
		runtime.SetProviderAuthHandler(m.handleAuthEvent)
	}
}

// SetProviderAuthHandler installs the process-wide callback for private auth
// notifications from provider sidecars. The handler must not block the RPC
// reader; the manager invokes it from a separate goroutine.
func (m *ProviderManager) SetProviderAuthHandler(fn func(ProviderAuthEvent)) {
	m.authHandlerMu.Lock()
	m.authHandler = fn
	m.authHandlerMu.Unlock()
	m.mu.Lock()
	clients := make([]*rpcClient, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	runtime := m.runtime
	m.mu.Unlock()
	if runtime != nil {
		runtime.SetProviderAuthHandler(m.handleAuthEvent)
	}
	for _, c := range clients {
		c.setProviderAuthHandler(m.handleAuthEvent)
	}
}

func (m *ProviderManager) handleAuthEvent(event ProviderAuthEvent) {
	m.authHandlerMu.RLock()
	fn := m.authHandler
	m.authHandlerMu.RUnlock()
	if fn != nil {
		fn(event)
	}
}

// Replace registers provider descriptors from globally enabled extensions.
// A provider runtime and its catalog/credential are shared by all sessions.
func (m *ProviderManager) Replace(descriptors []Descriptor) error {
	nextDescs := map[string]Descriptor{}
	nextSpecs := map[string]provider.ExtensionProviderSpec{}
	for _, d := range descriptors {
		if d.Error != "" || !d.Enabled || !hasKind(d.Capabilities, CapProvider) {
			continue
		}
		for _, spec := range d.Providers {
			if err := provider.ValidateExtensionProviderSpec(spec); err != nil {
				return fmt.Errorf("extension %q: %w", d.Name, err)
			}
			if previous, exists := nextDescs[spec.ID]; exists {
				return fmt.Errorf("provider %q declared by both %q and %q", spec.ID, previous.Name, d.Name)
			}
			nextDescs[spec.ID] = d
			nextSpecs[spec.ID] = spec
		}
	}

	m.mu.Lock()
	oldClients := m.clients
	oldDescs := m.descs
	m.epoch++
	m.clients = map[string]*rpcClient{}
	m.owners = map[string]string{}
	m.descs = nextDescs
	m.specs = nextSpecs
	for id, d := range nextDescs {
		m.owners[id] = d.Name
		if c := oldClients[d.Name]; c != nil && sameProviderRuntime(oldDescs[id], d) {
			m.clients[d.Name] = c
		}
	}
	m.mu.Unlock()

	for name, c := range oldClients {
		if m.clientByName(name) == c {
			continue
		}
		c.close()
	}
	return nil
}

func sameProviderRuntime(a, b Descriptor) bool {
	return a.Name == b.Name && a.Version == b.Version && a.Path == b.Path &&
		reflect.DeepEqual(a.Providers, b.Providers) && reflect.DeepEqual(a.manifest.Runtime, b.manifest.Runtime)
}

func (m *ProviderManager) clientByName(name string) *rpcClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clients[name]
}

// Specs returns the currently registered provider catalog in stable order.
func (m *ProviderManager) Specs() []provider.ExtensionProviderSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.specs))
	for id := range m.specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]provider.ExtensionProviderSpec, 0, len(ids))
	for _, id := range ids {
		spec := m.specs[id]
		spec.Models = append([]provider.ModelSeed(nil), spec.Models...)
		out = append(out, spec)
	}
	return out
}

// HasProvider reports whether a provider is owned by a registered sidecar.
func (m *ProviderManager) HasProvider(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.specs[id]
	return ok
}

// NewStreamer creates a host-side adapter for a provider sidecar stream.
func (m *ProviderManager) NewStreamer(model provider.Model, credential provider.Credential) provider.ProviderStreamer {
	return &providerSidecarStreamer{manager: m, model: model, credential: credential}
}

// Start launches provider runtimes together with ordinary extension runtimes.
// Model listing and the first stream therefore do not determine process
// startup timing.
func (m *ProviderManager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.runtime != nil {
		m.mu.Unlock()
		return
	}
	seen := map[string]Descriptor{}
	for _, d := range m.descs {
		seen[d.Name] = d
	}
	epoch := m.epoch
	m.mu.Unlock()
	for _, d := range seen {
		go m.start(ctx, d, epoch)
	}
}

func (m *ProviderManager) start(ctx context.Context, d Descriptor, epoch uint64) {
	m.mu.Lock()
	if m.epoch != epoch {
		m.mu.Unlock()
		return
	}
	for _, c := range m.clients {
		if c.name == d.Name {
			m.mu.Unlock()
			return
		}
	}
	m.mu.Unlock()
	c, err := startRPC(ctx, d, "", m.home, "", nil)
	if err != nil {
		m.mu.Lock()
		onErr := m.onErr
		m.mu.Unlock()
		if onErr != nil {
			onErr("", d.Name, string(CapProvider), "sidecar_start", err.Error())
		}
		return
	}
	c.setProviderAuthHandler(m.handleAuthEvent)
	m.mu.Lock()
	if m.epoch != epoch {
		m.mu.Unlock()
		c.close()
		return
	}
	for _, existing := range m.clients {
		if existing.name == d.Name {
			m.mu.Unlock()
			c.close()
			return
		}
	}
	m.clients[d.Name] = c
	m.mu.Unlock()
}

// StartAuth starts a provider-owned login flow. Progress is delivered through
// the handler installed with SetProviderAuthHandler.
func (m *ProviderManager) StartAuth(ctx context.Context, providerID, mode string) (string, error) {
	c, err := m.client(ctx, providerID)
	if err != nil {
		return "", err
	}
	requestID := "auth-" + fmt.Sprint(c.idSeq.Add(1))
	var result ProviderAuthResult
	if err := c.call(ctx, "provider.auth.start", ProviderAuthRequest{
		RequestID: requestID, Provider: providerID, Mode: mode,
	}, &result); err != nil {
		return "", err
	}
	if !result.Accepted {
		return "", fmt.Errorf("provider auth was not accepted")
	}
	return requestID, nil
}

// AuthInput supplies a manual redirect URL or authorization code to a login
// flow that advertised manual input support.
func (m *ProviderManager) AuthInput(ctx context.Context, providerID, requestID, value string) error {
	c, err := m.client(ctx, providerID)
	if err != nil {
		return err
	}
	var result ProviderAuthResult
	if err := c.call(ctx, "provider.auth.input", ProviderAuthRequest{
		RequestID: requestID, Provider: providerID, Value: value,
	}, &result); err != nil {
		return err
	}
	if !result.Accepted {
		return fmt.Errorf("provider auth input was not accepted")
	}
	return nil
}

// LockCredential serializes mutations of one provider's credential with
// refreshes. The returned function releases the lock.
func (m *ProviderManager) LockCredential(providerID string) func() {
	m.mu.Lock()
	lock := m.refreshLocks[providerID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.refreshLocks[providerID] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// CancelAuth asks a sidecar to abandon one login flow.
func (m *ProviderManager) CancelAuth(ctx context.Context, providerID, requestID string) error {
	c, err := m.client(ctx, providerID)
	if err != nil {
		return err
	}
	c.notify("provider.auth.cancel", ProviderAuthRequest{RequestID: requestID, Provider: providerID})
	return nil
}

// RefreshCredential lets the provider decide whether an opaque credential is
// near expiry. It serializes refreshes per provider so a rotating refresh
// token cannot be consumed concurrently by multiple sessions.
func (m *ProviderManager) RefreshCredential(ctx context.Context, registry *provider.Registry, providerID string, credential provider.Credential) (provider.Credential, error) {
	if credential.Type != provider.AuthOAuth {
		return credential, nil
	}
	unlockCredential := m.LockCredential(providerID)
	defer unlockCredential()

	// Re-read after waiting: another session may have persisted a fresh token
	// while this request was queued on the provider lock.
	if registry != nil {
		current, status, err := registry.Credential(providerID)
		if err != nil {
			return credential, err
		}
		if !status.Configured {
			return provider.Credential{}, fmt.Errorf("provider %q credential was removed", providerID)
		}
		credential = current
	}
	c, err := m.client(ctx, providerID)
	if err != nil {
		return credential, err
	}
	var result ProviderAuthResult
	refreshCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if err := c.call(refreshCtx, "provider.auth.refresh", ProviderAuthRequest{
		Provider: providerID, Credential: credential,
	}, &result); err != nil {
		return credential, err
	}
	if !result.Refreshed || result.Credential == nil || len(result.Credential.Value) == 0 {
		return credential, nil
	}
	if result.Credential.Type != provider.AuthOAuth {
		return credential, fmt.Errorf("provider returned a non-OAuth credential")
	}
	if registry != nil {
		current, status, err := registry.Credential(providerID)
		if err != nil {
			return credential, err
		}
		if !status.Configured {
			return provider.Credential{}, fmt.Errorf("provider %q credential was removed", providerID)
		}
		if current.Type != credential.Type || current.APIKey != credential.APIKey || string(current.Value) != string(credential.Value) {
			// A concurrent login/logout won the credential race. Do not let a
			// delayed refresh resurrect or overwrite the user's newer choice.
			return current, nil
		}
		if err := registry.SetCredentialValue(providerID, result.Credential.Type, result.Credential.Value); err != nil {
			return credential, fmt.Errorf("persist refreshed credential: %w", err)
		}
	}
	return *result.Credential, nil
}

// Close terminates all provider sidecars. It is safe to call more than once.
func (m *ProviderManager) Close() {
	// Clear identities before terminating processes so concurrent lookups fail
	// closed instead of attaching a new stream to a shutting-down sidecar.
	m.mu.Lock()
	clients := m.clients
	m.epoch++
	m.clients = map[string]*rpcClient{}
	m.owners = map[string]string{}
	m.descs = map[string]Descriptor{}
	m.specs = map[string]provider.ExtensionProviderSpec{}
	runtime := m.runtime
	m.mu.Unlock()
	if runtime != nil {
		return
	}
	for _, c := range clients {
		c.close()
	}
}

func (m *ProviderManager) client(ctx context.Context, providerID string) (*rpcClient, error) {
	m.mu.Lock()
	epoch := m.epoch
	runtime := m.runtime
	owner, ok := m.owners[providerID]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("provider %q is not registered by an extension", providerID)
	}
	if c := m.clients[owner]; c != nil {
		m.mu.Unlock()
		return c, nil
	}
	d, ok := m.descs[providerID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("provider %q extension descriptor is unavailable", providerID)
	}
	if runtime != nil {
		c, err := runtime.globalClient(ctx, d.Name)
		if err != nil {
			m.mu.Lock()
			onErr := m.onErr
			m.mu.Unlock()
			if onErr != nil {
				onErr("", d.Name, string(CapProvider), "sidecar_start", err.Error())
			}
			return nil, fmt.Errorf("start provider extension %q: %w", d.Name, err)
		}
		return c, nil
	}

	// Standalone ProviderManager users retain lazy startup; the server wires a
	// shared Manager above and starts it after the HTTP listener is ready.
	c, err := startRPC(ctx, d, "", m.home, "", nil)
	if err != nil {
		m.mu.Lock()
		onErr := m.onErr
		m.mu.Unlock()
		if onErr != nil {
			onErr("", d.Name, string(CapProvider), "sidecar_start", err.Error())
		}
		return nil, fmt.Errorf("start provider extension %q: %w", d.Name, err)
	}
	c.setProviderAuthHandler(m.handleAuthEvent)
	m.mu.Lock()
	if epoch != m.epoch {
		m.mu.Unlock()
		c.close()
		return nil, fmt.Errorf("provider runtime was reloaded")
	}
	if existing := m.clients[d.Name]; existing != nil {
		m.mu.Unlock()
		c.close()
		return existing, nil
	}
	m.clients[d.Name] = c
	m.mu.Unlock()
	slog.Debug("provider sidecar ready", "extension", d.Name, "provider", providerID)
	return c, nil
}

type providerSidecarStreamer struct {
	manager    *ProviderManager
	model      provider.Model
	credential provider.Credential
}

func (s *providerSidecarStreamer) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	c, err := s.manager.client(ctx, s.model.Provider)
	if err != nil {
		return types.Message{}, err
	}
	return c.streamProvider(ctx, ProviderStreamRequest{
		Provider:   s.model.Provider,
		Model:      s.model,
		Credential: s.credential,
		Request:    req,
	}, emit)
}
