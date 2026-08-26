package extension

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

// ProviderManager owns the process-level sidecars that implement provider
// runtimes. Unlike Manager, it is not keyed by session: one provider process
// can serve concurrent streams from many sessions.
type ProviderManager struct {
	mu      sync.Mutex
	home    string
	clients map[string]*rpcClient
	owners  map[string]string
	descs   map[string]Descriptor
	specs   map[string]provider.PluginSpec
	epoch   uint64
}

// NewProviderManager creates an empty process-level provider runtime manager.
func NewProviderManager(home string) *ProviderManager {
	return &ProviderManager{
		home:    home,
		clients: map[string]*rpcClient{},
		owners:  map[string]string{},
		descs:   map[string]Descriptor{},
		specs:   map[string]provider.PluginSpec{},
	}
}

// Replace registers the global provider descriptors from enabled extensions.
// Project-scoped provider descriptors are ignored intentionally in this first
// version: provider identity and credentials are process-global.
func (m *ProviderManager) Replace(descriptors []Descriptor) error {
	nextDescs := map[string]Descriptor{}
	nextSpecs := map[string]provider.PluginSpec{}
	for _, d := range descriptors {
		if d.Error != "" || !d.Enabled || !hasKind(d.Capabilities, CapProvider) {
			continue
		}
		if d.Scope != "" && d.Scope != "home" {
			continue
		}
		for _, spec := range d.Providers {
			if err := provider.ValidatePluginSpec(spec); err != nil {
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
func (m *ProviderManager) Specs() []provider.PluginSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.specs))
	for id := range m.specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]provider.PluginSpec, 0, len(ids))
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
	m.specs = map[string]provider.PluginSpec{}
	m.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

func (m *ProviderManager) client(ctx context.Context, providerID string) (*rpcClient, error) {
	m.mu.Lock()
	epoch := m.epoch
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

	// Startup is lazy so merely listing models does not execute third-party
	// code. The process is then retained and shared by all sessions.
	c, err := startRPC(ctx, d, "", m.home, "", nil)
	if err != nil {
		return nil, fmt.Errorf("start provider extension %q: %w", d.Name, err)
	}
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
