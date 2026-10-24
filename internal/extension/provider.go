package extension

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

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

func (m *ProviderManager) SetErrorHandler(fn ErrorFunc) {
	m.mu.Lock()
	m.onErr = fn
	m.mu.Unlock()
}

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
		runtime.Configure(descriptors)
		runtime.SetProviderAuthHandler(m.handleAuthEvent)
	}
}

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
				return fmt.Errorf("%w: %q by %q and %q", errProviderDeclaredTwice, spec.ID, previous.Name, d.Name)
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

func (m *ProviderManager) Specs() []provider.ExtensionProviderSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := slices.Sorted(maps.Keys(m.specs))
	out := make([]provider.ExtensionProviderSpec, 0, len(ids))
	for _, id := range ids {
		spec := m.specs[id]
		spec.Models = slices.Clone(spec.Models)
		out = append(out, spec)
	}
	return out
}

func (m *ProviderManager) HasProvider(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.specs[id]
	return ok
}

func (m *ProviderManager) NewStreamer(model provider.Model, credential provider.Credential) provider.Streamer {
	return &providerSidecarStreamer{manager: m, model: model, credential: credential}
}

func (m *ProviderManager) NewCompactor(model provider.Model, credential provider.Credential) provider.Compactor {
	return &providerSidecarStreamer{manager: m, model: model, credential: credential}
}

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
		return "", errProviderAuthNotAccepted
	}
	return requestID, nil
}

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
		return errProviderAuthInputNotAccepted
	}
	return nil
}

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

func (m *ProviderManager) CancelAuth(ctx context.Context, providerID, requestID string) error {
	c, err := m.client(ctx, providerID)
	if err != nil {
		return err
	}
	c.notify("provider.auth.cancel", ProviderAuthRequest{RequestID: requestID, Provider: providerID})
	return nil
}

func (m *ProviderManager) RefreshCredential(ctx context.Context, registry *provider.Registry, providerID string, credential provider.Credential) (provider.Credential, error) {
	if credential.Type != provider.AuthOAuth {
		return credential, nil
	}
	unlockCredential := m.LockCredential(providerID)
	defer unlockCredential()

	if registry != nil {
		current, status, err := registry.Credential(providerID)
		if err != nil {
			return credential, fmt.Errorf("read credential: %w", err)
		}
		if !status.Configured {
			return provider.Credential{}, fmt.Errorf("%w: %q", errProviderCredentialRemoved, providerID)
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
		return credential, errProviderNonOAuthCredential
	}
	if registry != nil {
		current, status, err := registry.Credential(providerID)
		if err != nil {
			return credential, fmt.Errorf("re-read credential: %w", err)
		}
		if !status.Configured {
			return provider.Credential{}, fmt.Errorf("%w: %q", errProviderCredentialRemoved, providerID)
		}
		if current.Type != credential.Type || current.APIKey != credential.APIKey || string(current.Value) != string(credential.Value) {
			return current, nil
		}
		if err := registry.SetCredentialValue(providerID, result.Credential.Type, result.Credential.Value); err != nil {
			return credential, fmt.Errorf("persist refreshed credential: %w", err)
		}
	}
	return *result.Credential, nil
}

func (m *ProviderManager) Close() {
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
		return nil, fmt.Errorf("%w: %q", errProviderNotRegisteredByExt, providerID)
	}
	if c := m.clients[owner]; c != nil {
		select {
		case <-c.closed:
			delete(m.clients, owner)
		default:
			m.mu.Unlock()
			return c, nil
		}
	}
	d, ok := m.descs[providerID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", errProviderExtDescriptorMissing, providerID)
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
		return nil, errProviderRuntimeReloaded
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
	msg, err := c.streamProvider(ctx, ProviderStreamRequest{
		Provider:   s.model.Provider,
		Model:      s.model,
		Credential: s.credential,
		Request:    req,
	}, emit)
	if msg.Usage != nil && msg.Usage.Cost == nil {
		provider.CalculateNormalizedCost(s.model, msg.Usage)
	}
	return msg, err
}

func (s *providerSidecarStreamer) Compact(ctx context.Context, req loop.Request) (provider.CompactResult, error) {
	c, err := s.manager.client(ctx, s.model.Provider)
	if err != nil {
		return provider.CompactResult{}, err
	}
	result, err := c.compactProvider(ctx, ProviderCompactRequest{
		Provider:   s.model.Provider,
		Model:      s.model,
		Credential: s.credential,
		Request:    req,
	})
	if err != nil {
		return provider.CompactResult{}, err
	}
	if result.Usage != nil && result.Usage.Cost == nil {
		provider.CalculateNormalizedCost(s.model, result.Usage)
	}
	return provider.CompactResult{Items: result.Items, Usage: result.Usage}, nil
}

var _ provider.Compactor = (*providerSidecarStreamer)(nil)
