package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var apiIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ModelRef identifies a provider and one of its models.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ModelsFile is the user-editable provider overlay document.
type ModelsFile struct {
	Version   int               `json:"version"`
	Default   ModelRef          `json:"default"`
	Providers map[string]Config `json:"providers"`
}

// Config describes user overrides for one provider.
type Config struct {
	Name           string                   `json:"name,omitempty"`
	API            string                   `json:"api,omitempty"`
	BaseURL        string                   `json:"baseUrl,omitempty"`
	Enabled        *bool                    `json:"enabled,omitempty"`
	Models         []ModelSeed              `json:"models,omitempty"`
	ModelOverrides map[string]ModelOverride `json:"modelOverrides,omitempty"`
}

// ModelOverride describes field-level overrides for one model.
type ModelOverride struct {
	Name               *string             `json:"name,omitempty"`
	Enabled            *bool               `json:"enabled,omitempty"`
	API                *string             `json:"api,omitempty"`
	BaseURL            *string             `json:"baseUrl,omitempty"`
	ContextWindow      *int                `json:"contextWindow,omitempty"`
	MaxTokens          *int                `json:"maxTokens,omitempty"`
	Input              *[]string           `json:"input,omitempty"`
	ApplyPatchToolType *string             `json:"applyPatchToolType,omitempty"`
	Reasoning          *bool               `json:"reasoning,omitempty"`
	ThinkingLevelMap   *map[string]*string `json:"thinkingLevelMap,omitempty"`
	Cost               json.RawMessage     `json:"cost,omitempty"`
	Compat             *Compat             `json:"compat,omitempty"`
}

type credentialEntry struct {
	Type   AuthKind        `json:"type,omitempty"`
	APIKey string          `json:"apiKey,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}
type credentialsFile struct {
	Version   int                        `json:"version"`
	Providers map[string]credentialEntry `json:"providers"`
}

// CredentialStatus reports whether a provider credential is available.
type CredentialStatus struct {
	Configured bool     `json:"configured"`
	Source     string   `json:"source,omitempty"`
	Type       AuthKind `json:"type,omitempty"`
}

// View combines a provider catalog entry with credential metadata.
type View struct {
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
	mu                     sync.RWMutex
	home                   string
	modelsPath             string
	credsPath              string
	state                  *registryState
	extensionProviders     map[string]Provider
	extensionProviderOrder []string
}

// NewRegistry loads the provider catalog and credentials rooted at home.
func NewRegistry(home string) (*Registry, error) {
	r := &Registry{
		home:                   home,
		modelsPath:             filepath.Join(home, "models.json"),
		credsPath:              filepath.Join(home, "credentials.json"),
		extensionProviders:     map[string]Provider{},
		extensionProviderOrder: []string{},
	}
	user := ModelsFile{Version: 1, Providers: map[string]Config{}}
	if err := readStrictJSON(r.modelsPath, &user); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if user.Version == 0 {
		user.Version = 1
	}
	if user.Providers == nil {
		user.Providers = map[string]Config{}
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

// ReplaceExtensionProviders replaces the ephemeral provider catalog supplied
// by enabled extensions. These entries are never written to models.json.
func (r *Registry) ReplaceExtensionProviders(specs []ExtensionProviderSpec) error {
	next := make(map[string]Provider, len(specs))
	order := make([]string, 0, len(specs))
	for _, spec := range specs {
		p, err := BuildExtensionProvider(spec)
		if err != nil {
			return err
		}
		if _, exists := next[p.ID]; exists {
			return fmt.Errorf("provider %q: %w", p.ID, errDuplicateProvider)
		}
		next[p.ID] = p
		order = append(order, p.ID)
	}
	r.mu.Lock()
	// An extension provider must not silently replace a built-in or user-configured
	// provider. Selection would otherwise depend on extension load order.
	for id := range next {
		if _, exists := r.state.providers[id]; exists {
			r.mu.Unlock()
			return fmt.Errorf("provider %q: %w", id, errDuplicateProvider)
		}
	}
	r.extensionProviders = next
	r.extensionProviderOrder = order
	r.state.defaultRef = r.defaultRefLocked()
	r.mu.Unlock()
	return nil
}

// ExtensionProvider reports whether a provider is implemented by an extension.
func (r *Registry) ExtensionProvider(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.extensionProviders[id]
	return ok
}

func (r *Registry) providerLocked(id string) (Provider, bool) {
	if p, ok := r.state.providers[id]; ok {
		return p, true
	}
	p, ok := r.extensionProviders[id]
	return p, ok
}

func (r *Registry) providerOrderLocked() []string {
	order := make([]string, 0, len(r.state.order)+len(r.extensionProviderOrder))
	order = append(order, r.state.order...)
	order = append(order, r.extensionProviderOrder...)
	return order
}

func (r *Registry) defaultRefLocked() ModelRef {
	providers := make(map[string]Provider, len(r.state.providers)+len(r.extensionProviders))
	maps.Copy(providers, r.state.providers)
	maps.Copy(providers, r.extensionProviders)
	return pickDefault(providers, r.providerOrderLocked(), r.state.creds, r.state.user.Default)
}

func readStrictJSON(path string, dst any) error {
	b, err := os.ReadFile(path) //nolint:gosec // path is a Ki-managed catalog or credentials file
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
			return fmt.Errorf("read %s: %w", path, errTrailingJSON)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func buildRegistryState(user ModelsFile, creds credentialsFile) (*registryState, error) {
	if user.Version != 1 {
		return nil, fmt.Errorf("models.json version %d is %w", user.Version, errUnsupportedVersion)
	}
	providers := map[string]Provider{}
	var order []string
	for _, p := range BuiltinProviders() {
		providers[p.ID] = p
		order = append(order, p.ID)
	}
	for id, overlay := range user.Providers {
		if !providerIDPattern.MatchString(id) {
			return nil, fmt.Errorf("provider %q: %w", id, errInvalidID)
		}
		p, builtin := providers[id]
		p.Customized = true
		originalAPI, originalBase := p.API, p.BaseURL
		if !builtin {
			if strings.TrimSpace(overlay.Name) == "" || overlay.API == "" || overlay.BaseURL == "" {
				return nil, fmt.Errorf("provider %q: %w", id, errProviderFieldsRequired)
			}
			p = Provider{ID: id, Auth: AuthSpec{Type: AuthAPIKey}, Enabled: true}
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
			if err := validateModel(m); err != nil {
				return nil, err
			}
			byID[m.ID] = m
			ids = append(ids, m.ID)
		}
		seenUserModels := map[string]bool{}
		for _, seed := range overlay.Models {
			if strings.TrimSpace(seed.ID) == "" {
				return nil, fmt.Errorf("provider %q: %w", id, errModelIDRequired)
			}
			if seenUserModels[seed.ID] {
				return nil, fmt.Errorf("provider %q: %w %q", id, errDuplicateModel, seed.ID)
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
				return nil, fmt.Errorf("provider %q: %w %q", id, errOverrideModelMissing, modelID)
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
			return nil, fmt.Errorf("provider %q: %w", id, errAtLeastOneModel)
		}
		if p.DefaultModel == "" && len(p.Models) > 0 {
			p.DefaultModel = p.Models[0].ID
		}
		providers[id] = p
	}
	def := pickDefault(providers, order, creds, user.Default)
	return &registryState{providers: providers, order: order, defaultRef: def, user: user, creds: creds}, nil
}

// pickDefault treats models.json default as last-used, not a pinned setting.
// If that ref is missing or disabled, fall back to the first enabled model
// with credentials, then the first enabled model in catalog order.
func pickDefault(providers map[string]Provider, order []string, creds credentialsFile, preferred ModelRef) ModelRef {
	if modelRefEnabled(providers, preferred) {
		return preferred
	}
	candidates := slices.Clone(ProviderOrder)
	for _, id := range order {
		if !slices.Contains(candidates, id) {
			candidates = append(candidates, id)
		}
	}
	firstEnabled := ModelRef{}
	for _, id := range candidates {
		ref := firstEnabledModel(providers[id])
		if ref.Model == "" {
			continue
		}
		if firstEnabled.Provider == "" {
			firstEnabled = ref
		}
		p := providers[id]
		if _, status := credentialFrom(creds, id, p.EnvVars); status.Configured {
			return ref
		}
	}
	return firstEnabled
}

func firstEnabledModel(p Provider) ModelRef {
	if !p.Enabled {
		return ModelRef{}
	}
	if p.DefaultModel != "" {
		for _, m := range p.Models {
			if m.ID == p.DefaultModel && m.Enabled {
				return ModelRef{Provider: p.ID, Model: m.ID}
			}
		}
	}
	for _, m := range p.Models {
		if m.Enabled {
			return ModelRef{Provider: p.ID, Model: m.ID}
		}
	}
	return ModelRef{}
}

func modelRefEnabled(providers map[string]Provider, ref ModelRef) bool {
	p, ok := providers[ref.Provider]
	if !ok || !p.Enabled {
		return false
	}
	for _, m := range p.Models {
		if m.ID == ref.Model && m.Enabled {
			return true
		}
	}
	return false
}

func validateProviderShape(p Provider) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("provider %q: %w", p.ID, errNameRequired)
	}
	if !validAPI(p.API) {
		return fmt.Errorf("provider %q: %w %q", p.ID, errInvalidAPI, p.API)
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider %q: %w", p.ID, errInvalidBaseURL)
	}
	return nil
}
func validAPI(api string) bool {
	return api == "completions" || api == "responses" || api == "anthropic"
}

// ValidateExtensionProviderSpec validates a provider catalog supplied by an
// extension. Extension APIs are intentionally open-ended; the owning sidecar, rather than
// the built-in HTTP adapters, is responsible for implementing them.
func ValidateExtensionProviderSpec(spec ExtensionProviderSpec) error {
	if !providerIDPattern.MatchString(spec.ID) {
		return fmt.Errorf("provider %q: %w", spec.ID, errInvalidID)
	}
	if strings.TrimSpace(spec.Name) == "" {
		return fmt.Errorf("provider %q: %w", spec.ID, errNameRequired)
	}
	if !apiIDPattern.MatchString(spec.API) {
		return fmt.Errorf("provider %q: %w %q", spec.ID, errInvalidAPI, spec.API)
	}
	u, err := url.Parse(spec.BaseURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider %q: %w", spec.ID, errInvalidBaseURL)
	}
	if len(spec.Models) == 0 {
		return fmt.Errorf("provider %q: %w", spec.ID, errAtLeastOneModel)
	}
	if spec.Auth.Type == "" {
		spec.Auth.Type = AuthAPIKey
	}
	if spec.Auth.Type != AuthNone && spec.Auth.Type != AuthAPIKey && spec.Auth.Type != AuthOAuth {
		return fmt.Errorf("provider %q: %w %q", spec.ID, errUnsupportedAuthType, spec.Auth.Type)
	}
	seenModels := make(map[string]bool, len(spec.Models))
	for _, seed := range spec.Models {
		model := resolveSeed(spec.ID, spec.API, spec.BaseURL, seed, false)
		if seenModels[model.ID] {
			return fmt.Errorf("provider %q: %w %q", spec.ID, errDuplicateModel, model.ID)
		}
		seenModels[model.ID] = true
		if err := validateExtensionModel(model); err != nil {
			return err
		}
	}
	// The manifest's defaultModel is only a preference. Extensions commonly
	// trim their model list without updating that field; BuildExtensionProvider
	// deliberately falls back to the first enabled model instead of making the
	// entire provider disappear.
	return nil
}

func validateExtensionModel(m Model) error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("provider %q: %w", m.Provider, errModelIDRequired)
	}
	if !apiIDPattern.MatchString(m.API) {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errInvalidAPI)
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errInvalidBaseURL)
	}
	if m.ContextWindow <= 0 || m.MaxTokens <= 0 {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errTokenLimitsPositive)
	}
	if !slices.Contains(m.Input, "text") {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errTextInputRequired)
	}
	if m.ApplyPatchToolType != "" && m.ApplyPatchToolType != "freeform" {
		return fmt.Errorf("provider %q model %q: %w %q", m.Provider, m.ID, errInvalidPatchToolType, m.ApplyPatchToolType)
	}
	for k := range m.ThinkingLevelMap {
		if !slices.Contains(thinkingLevels, k) {
			return fmt.Errorf("provider %q model %q: %w %q", m.Provider, m.ID, errInvalidThinkingLevel, k)
		}
	}
	if m.Cost != nil {
		if err := validateRates(m.Cost.CostRates); err != nil {
			return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, err)
		}
		last := -1
		for _, tier := range m.Cost.Tiers {
			if tier.InputTokensAbove <= last {
				return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errCostTierOrder)
			}
			if err := validateRates(tier.CostRates); err != nil {
				return fmt.Errorf("provider %q model %q tier: %w", m.Provider, m.ID, err)
			}
			last = tier.InputTokensAbove
		}
	}
	return nil
}

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func validateModel(m Model) error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("provider %q: %w", m.Provider, errModelIDRequired)
	}
	if !validAPI(m.API) {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errInvalidAPI)
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errInvalidBaseURL)
	}
	if m.ContextWindow <= 0 || m.MaxTokens <= 0 {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errTokenLimitsPositive)
	}
	if !slices.Contains(m.Input, "text") {
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errTextInputRequired)
	}
	if m.ApplyPatchToolType != "" && m.ApplyPatchToolType != "freeform" {
		return fmt.Errorf("provider %q model %q: %w %q", m.Provider, m.ID, errInvalidPatchToolType, m.ApplyPatchToolType)
	}
	if m.ApplyPatchToolType == "freeform" && m.API != "responses" {
		// Codex freeform calls use Responses custom_tool_call wire items; the
		// other adapters can only describe JSON function tools.
		return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errFreeformResponsesAPI)
	}
	for k := range m.ThinkingLevelMap {
		if !slices.Contains(thinkingLevels, k) {
			return fmt.Errorf("provider %q model %q: %w %q", m.Provider, m.ID, errInvalidThinkingLevel, k)
		}
	}
	if m.Cost != nil {
		if err := validateRates(m.Cost.CostRates); err != nil {
			return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, err)
		}
		last := -1
		for _, tier := range m.Cost.Tiers {
			if tier.InputTokensAbove <= last {
				return fmt.Errorf("provider %q model %q: %w", m.Provider, m.ID, errCostTierOrder)
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
		return errNegativeCostRates
	}
	return nil
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
	if o.ApplyPatchToolType != nil {
		m.ApplyPatchToolType = *o.ApplyPatchToolType
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

// Providers returns the resolved providers in stable presentation order.
func (r *Registry) Providers() []View {
	r.mu.RLock()
	defer r.mu.RUnlock()
	order := r.providerOrderLocked()
	out := make([]View, 0, len(order))
	for _, id := range order {
		p, ok := r.providerLocked(id)
		if !ok {
			continue
		}
		p = cloneProvider(p)
		_, status := r.credentialLockedFor(p)
		out = append(out, View{Provider: p, Credential: status})
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

// Default returns the last selected provider/model reference.
func (r *Registry) Default() ModelRef { r.mu.RLock(); defer r.mu.RUnlock(); return r.state.defaultRef }

// RememberDefault records the last selected model. No-op when unchanged.
// Why: there is no pinned "set as default"; switching a model is what
// subsequent creates fall back to, and a stale last-used is skipped by
// pickDefault instead of blocking disable/delete.
func (r *Registry) RememberDefault(ref ModelRef) error {
	if ref.Provider == "" || ref.Model == "" {
		return nil
	}
	r.mu.RLock()
	cur := r.state.user.Default
	r.mu.RUnlock()
	if cur.Provider == ref.Provider && cur.Model == ref.Model {
		return nil
	}
	return r.Update(func(cfg *ModelsFile) error {
		cfg.Default = ref
		return nil
	})
}

// FindModel returns an enabled model without requiring a provider credential.
func (r *Registry) FindModel(providerID, modelID string) (Provider, Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providerLocked(providerID)
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

// ResolveSpec resolves a model specification against the registry default.
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
	p, ok := r.providerLocked(ref.Provider)
	if !ok || !p.Enabled {
		return ModelRef{}, Model{}, fmt.Errorf("provider %q is %w", ref.Provider, errUnavailable)
	}
	for _, m := range p.Models {
		if m.ID == ref.Model && m.Enabled {
			return ref, m, nil
		}
	}
	return ModelRef{}, Model{}, fmt.Errorf("model %q/%q is %w", ref.Provider, ref.Model, errUnavailable)
}

// Resolve returns an enabled model and its configured API credential.
func (r *Registry) Resolve(providerID, modelID string) (Provider, Model, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providerLocked(providerID)
	if !ok || !p.Enabled {
		return Provider{}, Model{}, "", fmt.Errorf("provider %q is %w", providerID, errUnavailable)
	}
	for _, m := range p.Models {
		if m.ID == modelID {
			if !m.Enabled {
				break
			}
			key, status := r.credentialLockedFor(p)
			if !status.Configured {
				return Provider{}, Model{}, "", fmt.Errorf("provider %q %w", providerID, errNoAPIKey)
			}
			return cloneProvider(p), m, key, nil
		}
	}
	return Provider{}, Model{}, "", fmt.Errorf("model %q/%q is %w", providerID, modelID, errUnavailable)
}

func (r *Registry) credentialLockedFor(p Provider) (string, CredentialStatus) {
	if p.Auth.Type == AuthNone {
		return "", CredentialStatus{Configured: true, Source: "none", Type: AuthNone}
	}
	return credentialFrom(r.state.creds, p.ID, p.EnvVars)
}

func credentialFrom(creds credentialsFile, id string, envVars []string) (string, CredentialStatus) {
	if c := creds.Providers[id]; strings.TrimSpace(c.APIKey) != "" {
		typ := c.Type
		if typ == "" {
			typ = AuthAPIKey
		}
		return c.APIKey, CredentialStatus{Configured: true, Source: "stored", Type: typ}
	}
	if c := creds.Providers[id]; c.Type != "" && len(bytes.TrimSpace(c.Value)) > 0 && !bytes.Equal(bytes.TrimSpace(c.Value), []byte("null")) {
		return "", CredentialStatus{Configured: true, Source: "stored", Type: c.Type}
	}
	for _, name := range envVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, CredentialStatus{Configured: true, Source: name, Type: AuthAPIKey}
		}
	}
	return "", CredentialStatus{}
}

// Credential resolves the provider-owned credential without exposing it in
// provider catalog views. Environment API keys are represented as api_key
// credentials; opaque values are returned exactly as stored.
func (r *Registry) Credential(id string) (Credential, CredentialStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providerLocked(id)
	if !ok {
		return Credential{}, CredentialStatus{}, fmt.Errorf("provider %q: %w", id, errProviderNotFound)
	}
	if p.Auth.Type == AuthNone {
		return Credential{Type: AuthNone}, CredentialStatus{Configured: true, Source: "none", Type: AuthNone}, nil
	}
	if c := r.state.creds.Providers[id]; strings.TrimSpace(c.APIKey) != "" {
		typ := c.Type
		if typ == "" {
			typ = AuthAPIKey
		}
		return Credential{Type: typ, APIKey: c.APIKey}, CredentialStatus{Configured: true, Source: "stored", Type: typ}, nil
	}
	if c := r.state.creds.Providers[id]; c.Type != "" && len(bytes.TrimSpace(c.Value)) > 0 && !bytes.Equal(bytes.TrimSpace(c.Value), []byte("null")) {
		return Credential{Type: c.Type, Value: append(json.RawMessage(nil), c.Value...)}, CredentialStatus{Configured: true, Source: "stored", Type: c.Type}, nil
	}
	for _, name := range p.EnvVars {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return Credential{Type: AuthAPIKey, APIKey: v}, CredentialStatus{Configured: true, Source: name, Type: AuthAPIKey}, nil
		}
	}
	return Credential{}, CredentialStatus{}, nil
}

// Models returns enabled models, optionally limited to credentialed providers.
func (r *Registry) Models(availableOnly bool) []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Model
	for _, id := range r.providerOrderLocked() {
		p, ok := r.providerLocked(id)
		if !ok {
			continue
		}
		_, auth := r.credentialLockedFor(p)
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

// UserConfig returns a detached copy of the user provider overlay.
func (r *Registry) UserConfig() ModelsFile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneModelsFile(r.state.user)
}
func cloneModelsFile(in ModelsFile) ModelsFile {
	b, err := json.Marshal(in)
	if err != nil {
		panic(fmt.Errorf("clone models file: %w", err))
	}
	var out ModelsFile
	if err := json.Unmarshal(b, &out); err != nil {
		panic(fmt.Errorf("unmarshal cloned models file: %w", err))
	}
	return out
}

// Update validates and atomically persists a mutation to the user overlay.
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
	r.state.defaultRef = r.defaultRefLocked()
	return nil
}

// SetCredential stores or removes the credential for one provider.
func (r *Registry) SetCredential(id string, key *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providerLocked(id)
	if !ok {
		return fmt.Errorf("provider %q: %w", id, errProviderNotFound)
	}
	next := r.state.creds
	next.Providers = mapsCloneCredentials(next.Providers)
	switch {
	case key == nil:
		delete(next.Providers, id)
	case strings.TrimSpace(*key) == "":
		return errAPIKeyRequired
	default:
		if p.Auth.Type == AuthOAuth {
			return fmt.Errorf("provider %q: use an oauth credential value: %w", id, errCredentialType)
		}
		typ := p.Auth.Type
		if typ == "" || typ == AuthNone {
			typ = AuthAPIKey
		}
		next.Providers[id] = credentialEntry{Type: typ, APIKey: *key}
	}
	if err := writeJSONAtomic(r.credsPath, next, 0o600); err != nil {
		return err
	}
	state, err := buildRegistryState(r.state.user, next)
	if err != nil {
		return err
	}
	r.state = state
	r.state.defaultRef = r.defaultRefLocked()
	return nil
}

// SetCredentialValue stores an opaque provider-owned credential, such as an
// OAuth token bundle. The value is not interpreted by the registry.
func (r *Registry) SetCredentialValue(id string, typ AuthKind, value json.RawMessage) error {
	if typ == "" || typ == AuthNone {
		return errAPIKeyRequired
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errAPIKeyRequired
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providerLocked(id)
	if !ok {
		return fmt.Errorf("provider %q: %w", id, errProviderNotFound)
	}
	if p.Auth.Type != typ {
		return fmt.Errorf("provider %q: %w (want %s, got %s)", id, errCredentialType, p.Auth.Type, typ)
	}
	next := r.state.creds
	next.Providers = mapsCloneCredentials(next.Providers)
	next.Providers[id] = credentialEntry{Type: typ, Value: append(json.RawMessage(nil), trimmed...)}
	if err := writeJSONAtomic(r.credsPath, next, 0o600); err != nil {
		return err
	}
	state, err := buildRegistryState(r.state.user, next)
	if err != nil {
		return err
	}
	r.state = state
	r.state.defaultRef = r.defaultRefLocked()
	return nil
}
func mapsCloneCredentials(in map[string]credentialEntry) map[string]credentialEntry {
	out := make(map[string]credentialEntry, len(in))
	maps.Copy(out, in)
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
	defer func() { _ = os.Remove(tmp) }()
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

// ClampThinking maps a requested effort to the nearest supported level.
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
		return "", fmt.Errorf("%w %q", errInvalidThinkingEffort, requested)
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
	return "", errNoThinkingEffort
}
