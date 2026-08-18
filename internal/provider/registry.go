package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ModelsFile struct {
	Version   int                       `json:"version"`
	Default   ModelRef                  `json:"default"`
	Providers map[string]ProviderConfig `json:"providers"`
}

type ProviderConfig struct {
	Name           string                   `json:"name,omitempty"`
	API            string                   `json:"api,omitempty"`
	BaseURL        string                   `json:"baseUrl,omitempty"`
	Enabled        *bool                    `json:"enabled,omitempty"`
	Models         []ModelSeed              `json:"models,omitempty"`
	ModelOverrides map[string]ModelOverride `json:"modelOverrides,omitempty"`
}

type ModelOverride struct {
	Name             *string             `json:"name,omitempty"`
	Enabled          *bool               `json:"enabled,omitempty"`
	API              *string             `json:"api,omitempty"`
	BaseURL          *string             `json:"baseUrl,omitempty"`
	ContextWindow    *int                `json:"contextWindow,omitempty"`
	MaxTokens        *int                `json:"maxTokens,omitempty"`
	Input            *[]string           `json:"input,omitempty"`
	Reasoning        *bool               `json:"reasoning,omitempty"`
	ThinkingLevelMap *map[string]*string `json:"thinkingLevelMap,omitempty"`
	Cost             json.RawMessage     `json:"cost,omitempty"`
	Compat           *Compat             `json:"compat,omitempty"`
}

type credentialEntry struct {
	APIKey string `json:"apiKey"`
}
type credentialsFile struct {
	Version   int                        `json:"version"`
	Providers map[string]credentialEntry `json:"providers"`
}

type CredentialStatus struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
}

type ProviderView struct {
	Provider
	Credential CredentialStatus `json:"credential"`
}

type registryState struct {
	providers  map[string]Provider
	order      []string
	defaultRef ModelRef
	user       ModelsFile
	creds      credentialsFile
}

// Registry owns the offline model catalog and mutable global overlays.
type Registry struct {
	mu         sync.RWMutex
	home       string
	modelsPath string
	credsPath  string
	state      *registryState
}

func NewRegistry(home string) (*Registry, error) {
	r := &Registry{home: home, modelsPath: filepath.Join(home, "models.json"), credsPath: filepath.Join(home, "credentials.json")}
	user := ModelsFile{Version: 1, Providers: map[string]ProviderConfig{}}
	if err := readStrictJSON(r.modelsPath, &user); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if user.Version == 0 {
		user.Version = 1
	}
	if user.Providers == nil {
		user.Providers = map[string]ProviderConfig{}
	}
	creds := credentialsFile{Version: 1, Providers: map[string]credentialEntry{}}
	if err := readStrictJSON(r.credsPath, &creds); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if creds.Version == 0 {
		creds.Version = 1
	}
	if creds.Providers == nil {
		creds.Providers = map[string]credentialEntry{}
	}
	state, err := buildRegistryState(user, creds)
	if err != nil {
		return nil, err
	}
	r.state = state
	return r, nil
}

func readStrictJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("read %s: trailing JSON", path)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func buildRegistryState(user ModelsFile, creds credentialsFile) (*registryState, error) {
	if user.Version != 1 {
		return nil, fmt.Errorf("models.json version %d is unsupported", user.Version)
	}
	providers := map[string]Provider{}
	var order []string
	for _, p := range BuiltinProviders() {
		providers[p.ID] = p
		order = append(order, p.ID)
	}
	for id, overlay := range user.Providers {
		if !providerIDPattern.MatchString(id) {
			return nil, fmt.Errorf("provider %q: invalid id", id)
		}
		p, builtin := providers[id]
		p.Customized = true
		originalAPI, originalBase := p.API, p.BaseURL
		if !builtin {
			if strings.TrimSpace(overlay.Name) == "" || overlay.API == "" || overlay.BaseURL == "" {
				return nil, fmt.Errorf("provider %q: name, api and baseUrl required", id)
			}
			p = Provider{ID: id, Enabled: true}
			order = append(order, id)
		}
		if overlay.Name != "" {
			p.Name = overlay.Name
		}
		if overlay.API != "" {
			p.API = overlay.API
		}
		if overlay.BaseURL != "" {
			p.BaseURL = strings.TrimRight(overlay.BaseURL, "/")
		}
		if overlay.Enabled != nil {
			p.Enabled = *overlay.Enabled
		}
		if builtin {
			for i := range p.Models {
				if p.Models[i].API == originalAPI {
					p.Models[i].API = p.API
				}
				if p.Models[i].BaseURL == originalBase {
					p.Models[i].BaseURL = p.BaseURL
				}
			}
		}
		if err := validateProviderShape(p); err != nil {
			return nil, err
		}
		byID := map[string]Model{}
		var ids []string
		for _, m := range p.Models {
			byID[m.ID] = m
			ids = append(ids, m.ID)
		}
		seenUserModels := map[string]bool{}
		for _, seed := range overlay.Models {
			if strings.TrimSpace(seed.ID) == "" {
				return nil, fmt.Errorf("provider %q: model id required", id)
			}
			if seenUserModels[seed.ID] {
				return nil, fmt.Errorf("provider %q: duplicate model %q", id, seed.ID)
			}
			seenUserModels[seed.ID] = true
			m := resolveSeed(id, p.API, p.BaseURL, seed, false)
			if err := validateModel(m); err != nil {
				return nil, err
			}
			if _, ok := byID[m.ID]; !ok {
				ids = append(ids, m.ID)
			}
			byID[m.ID] = m
		}
		for modelID, override := range overlay.ModelOverrides {
			m, ok := byID[modelID]
			if !ok {
				return nil, fmt.Errorf("provider %q: override model %q not found", id, modelID)
			}
			var err error
			m, err = applyModelOverride(m, override)
			if err != nil {
				return nil, fmt.Errorf("provider %q model %q: %w", id, modelID, err)
			}
			if err := validateModel(m); err != nil {
				return nil, err
			}
			byID[modelID] = m
		}
		p.Models = make([]Model, 0, len(ids))
		for _, modelID := range ids {
			p.Models = append(p.Models, byID[modelID])
		}
		if !builtin && len(p.Models) == 0 {
			return nil, fmt.Errorf("provider %q: at least one model required", id)
		}
		if p.DefaultModel == "" && len(p.Models) > 0 {
			p.DefaultModel = p.Models[0].ID
		}
		providers[id] = p
	}
	def := user.Default
	if def.Provider == "" {
		// With no explicit default, prefer the first enabled provider that is
		// usable on this machine, then fall back to the stable catalog order.
		candidates := slices.Clone(ProviderOrder)
		for _, id := range order {
			if !slices.Contains(candidates, id) {
				candidates = append(candidates, id)
			}
		}
		for _, id := range candidates {
			p := providers[id]
			if p.Enabled {
				if _, status := credentialFrom(creds, id, p.EnvVars); status.Configured {
					def = ModelRef{Provider: id, Model: p.DefaultModel}
					break
				}
			}
		}
	}
	if def.Provider == "" {
		for _, id := range ProviderOrder {
			if p, ok := providers[id]; ok {
				def = ModelRef{Provider: id, Model: p.DefaultModel}
				break
			}
		}
	}
	if def.Provider != "" {
		if err := validateDefault(providers, def); err != nil {
			return nil, err
		}
	}
	return &registryState{providers: providers, order: order, defaultRef: def, user: user, creds: creds}, nil
}

func validateProviderShape(p Provider) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("provider %q: name required", p.ID)
	}
	if !validAPI(p.API) {
		return fmt.Errorf("provider %q: invalid api %q", p.ID, p.API)
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.IsAbs() == false || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider %q: invalid baseUrl", p.ID)
	}
	return nil
}
func validAPI(api string) bool {
	return api == "completions" || api == "responses" || api == "anthropic"
}

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func validateModel(m Model) error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("provider %q: model id required", m.Provider)
	}
	if !validAPI(m.API) {
		return fmt.Errorf("provider %q model %q: invalid api", m.Provider, m.ID)
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider %q model %q: invalid baseUrl", m.Provider, m.ID)
	}
	if m.ContextWindow <= 0 || m.MaxTokens <= 0 {
		return fmt.Errorf("provider %q model %q: token limits must be positive", m.Provider, m.ID)
	}
	if !slices.Contains(m.Input, "text") {
		return fmt.Errorf("provider %q model %q: input must include text", m.Provider, m.ID)
	}
	for k := range m.ThinkingLevelMap {
		if !slices.Contains(thinkingLevels, k) {
			return fmt.Errorf("provider %q model %q: invalid thinking level %q", m.Provider, m.ID, k)
		}
	}
	if m.Cost != nil {
		if err := validateRates(m.Cost.CostRates); err != nil {
			return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, err)
		}
		last := -1
		for _, tier := range m.Cost.Tiers {
			if tier.InputTokensAbove <= last {
				return fmt.Errorf("provider %q model %q: cost tiers must have increasing thresholds", m.Provider, m.ID)
			}
			if err := validateRates(tier.CostRates); err != nil {
				return fmt.Errorf("provider %q model %q tier: %w", m.Provider, m.ID, err)
			}
			last = tier.InputTokensAbove
		}
	}
	return nil
}

func validateRates(r CostRates) error {
	if r.Input < 0 || r.Output < 0 || r.CacheRead < 0 || r.CacheWrite < 0 {
		return errors.New("cost rates must not be negative")
	}
	return nil
}

func validateDefault(providers map[string]Provider, ref ModelRef) error {
	p, ok := providers[ref.Provider]
	if !ok || !p.Enabled {
		return fmt.Errorf("default provider %q is unavailable", ref.Provider)
	}
	for _, m := range p.Models {
		if m.ID == ref.Model && m.Enabled {
			return nil
		}
	}
	return fmt.Errorf("default model %q/%q is unavailable", ref.Provider, ref.Model)
}

func applyModelOverride(m Model, o ModelOverride) (Model, error) {
	m.Customized = true
	if o.Name != nil {
		m.Name = *o.Name
	}
	if o.Enabled != nil {
		m.Enabled = *o.Enabled
	}
	if o.API != nil {
		m.API = *o.API
	}
	if o.BaseURL != nil {
		m.BaseURL = strings.TrimRight(*o.BaseURL, "/")
	}
	if o.ContextWindow != nil {
		m.ContextWindow = *o.ContextWindow
	}
	if o.MaxTokens != nil {
		m.MaxTokens = *o.MaxTokens
	}
	if o.Input != nil {
		m.Input = slices.Clone(*o.Input)
	}
	if o.Reasoning != nil {
		m.Reasoning = *o.Reasoning
	}
	if o.ThinkingLevelMap != nil {
		m.ThinkingLevelMap = cloneThinkingMap(*o.ThinkingLevelMap)
	}
	if o.Compat != nil {
		m.Compat = *o.Compat
	}
	if o.Cost != nil {
		if bytes.Equal(bytes.TrimSpace(o.Cost), []byte("null")) {
			m.Cost = nil
		} else {
			var c Cost
			if err := json.Unmarshal(o.Cost, &c); err != nil {
				return m, err
			}
			m.Cost = &c
		}
	}
	return m, nil
}

func (r *Registry) Providers() []ProviderView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ProviderView, 0, len(r.state.order))
	for _, id := range r.state.order {
		p := cloneProvider(r.state.providers[id])
		_, status := r.credentialLocked(id, p.EnvVars)
		out = append(out, ProviderView{Provider: p, Credential: status})
	}
	return out
}

func cloneProvider(p Provider) Provider {
	p.EnvVars = slices.Clone(p.EnvVars)
	p.Models = slices.Clone(p.Models)
	for i := range p.Models {
		p.Models[i].Input = slices.Clone(p.Models[i].Input)
		p.Models[i].ThinkingLevelMap = cloneThinkingMap(p.Models[i].ThinkingLevelMap)
		p.Models[i].Cost = cloneCost(p.Models[i].Cost)
	}
	return p
}

func (r *Registry) Default() ModelRef { r.mu.RLock(); defer r.mu.RUnlock(); return r.state.defaultRef }

func (r *Registry) FindModel(providerID, modelID string) (Provider, Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.state.providers[providerID]
	if !ok || !p.Enabled {
		return Provider{}, Model{}, false
	}
	for _, m := range p.Models {
		if m.ID == modelID && m.Enabled {
			return cloneProvider(p), m, true
		}
	}
	return Provider{}, Model{}, false
}

func (r *Registry) ResolveSpec(spec, fallbackProvider string) (ModelRef, Model, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ref := r.state.defaultRef
	if spec != "" {
		if p, m, ok := strings.Cut(spec, "/"); ok {
			ref = ModelRef{Provider: p, Model: m}
		} else if fallbackProvider != "" {
			ref = ModelRef{Provider: fallbackProvider, Model: spec}
		} else {
			ref.Model = spec
		}
	}
	p, ok := r.state.providers[ref.Provider]
	if !ok || !p.Enabled {
		return ModelRef{}, Model{}, fmt.Errorf("provider %q is unavailable", ref.Provider)
	}
	for _, m := range p.Models {
		if m.ID == ref.Model && m.Enabled {
			return ref, m, nil
		}
	}
	return ModelRef{}, Model{}, fmt.Errorf("model %q/%q is unavailable", ref.Provider, ref.Model)
}

func (r *Registry) Resolve(providerID, modelID string) (Provider, Model, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.state.providers[providerID]
	if !ok || !p.Enabled {
		return Provider{}, Model{}, "", fmt.Errorf("provider %q is unavailable", providerID)
	}
	for _, m := range p.Models {
		if m.ID == modelID {
			if !m.Enabled {
				break
			}
			key, status := r.credentialLocked(providerID, p.EnvVars)
			if !status.Configured {
				return Provider{}, Model{}, "", fmt.Errorf("provider %q has no API key", providerID)
			}
			return cloneProvider(p), m, key, nil
		}
	}
	return Provider{}, Model{}, "", fmt.Errorf("model %q/%q is unavailable", providerID, modelID)
}

func (r *Registry) credentialLocked(id string, envVars []string) (string, CredentialStatus) {
	return credentialFrom(r.state.creds, id, envVars)
}

func credentialFrom(creds credentialsFile, id string, envVars []string) (string, CredentialStatus) {
	if c := creds.Providers[id]; strings.TrimSpace(c.APIKey) != "" {
		return c.APIKey, CredentialStatus{Configured: true, Source: "stored"}
	}
	for _, name := range envVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, CredentialStatus{Configured: true, Source: name}
		}
	}
	return "", CredentialStatus{}
}

func (r *Registry) Models(availableOnly bool) []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Model
	for _, id := range r.state.order {
		p := r.state.providers[id]
		_, auth := r.credentialLocked(id, p.EnvVars)
		if !p.Enabled || (availableOnly && !auth.Configured) {
			continue
		}
		for _, m := range p.Models {
			if m.Enabled {
				out = append(out, m)
			}
		}
	}
	return out
}

func (r *Registry) UserConfig() ModelsFile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneModelsFile(r.state.user)
}
func cloneModelsFile(in ModelsFile) ModelsFile {
	b, _ := json.Marshal(in)
	var out ModelsFile
	_ = json.Unmarshal(b, &out)
	return out
}

func (r *Registry) Update(mut func(*ModelsFile) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneModelsFile(r.state.user)
	if err := mut(&next); err != nil {
		return err
	}
	next.Version = 1
	state, err := buildRegistryState(next, r.state.creds)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(r.modelsPath, next, 0o600); err != nil {
		return err
	}
	r.state = state
	return nil
}

func (r *Registry) SetCredential(id string, key *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.state.providers[id]; !ok {
		return fmt.Errorf("provider %q not found", id)
	}
	next := r.state.creds
	next.Providers = mapsCloneCredentials(next.Providers)
	if key == nil {
		delete(next.Providers, id)
	} else if strings.TrimSpace(*key) == "" {
		return errors.New("apiKey must not be empty")
	} else {
		next.Providers[id] = credentialEntry{APIKey: *key}
	}
	if err := writeJSONAtomic(r.credsPath, next, 0o600); err != nil {
		return err
	}
	state, err := buildRegistryState(r.state.user, next)
	if err != nil {
		return err
	}
	r.state = state
	return nil
}
func mapsCloneCredentials(in map[string]credentialEntry) map[string]credentialEntry {
	out := make(map[string]credentialEntry, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func writeJSONAtomic(path string, value any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(perm); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(tmp, path)
}

// SupportedThinkingLevels follows pi: standard levels are implicit; xhigh/max require explicit mappings.
func SupportedThinkingLevels(m Model) []string {
	if !m.Reasoning {
		return []string{"off"}
	}
	var out []string
	for _, level := range thinkingLevels {
		mapped, exists := m.ThinkingLevelMap[level]
		if exists && mapped == nil {
			continue
		}
		if (level == "xhigh" || level == "max") && !exists {
			continue
		}
		out = append(out, level)
	}
	return out
}

// DefaultThinking follows pi's useful default while respecting model maps.
func DefaultThinking(m Model) string {
	supported := SupportedThinkingLevels(m)
	if slices.Contains(supported, "medium") {
		return "medium"
	}
	if len(supported) > 0 {
		return supported[0]
	}
	return ""
}

func ClampThinking(m Model, requested string) (string, error) {
	if requested == "" {
		return "", nil
	}
	supported := SupportedThinkingLevels(m)
	if slices.Contains(supported, requested) {
		return requested, nil
	}
	idx := slices.Index(thinkingLevels, requested)
	if idx < 0 {
		return "", fmt.Errorf("invalid thinking effort %q", requested)
	}
	for i := idx; i < len(thinkingLevels); i++ {
		if slices.Contains(supported, thinkingLevels[i]) {
			return thinkingLevels[i], nil
		}
	}
	for i := idx - 1; i >= 0; i-- {
		if slices.Contains(supported, thinkingLevels[i]) {
			return thinkingLevels[i], nil
		}
	}
	return "", errors.New("model has no supported thinking effort")
}
