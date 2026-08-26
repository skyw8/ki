package provider

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// AuthKind identifies the credential protocol owned by a provider runtime.
type AuthKind string

const (
	AuthNone   AuthKind = "none"
	AuthAPIKey AuthKind = "api_key"
	AuthOAuth  AuthKind = "oauth"
)

// AuthSpec describes provider authentication without exposing credentials.
type AuthSpec struct {
	Type         AuthKind `json:"type,omitempty"`
	Name         string   `json:"name,omitempty"`
	Subscription bool     `json:"subscription,omitempty"`
}

// Credential is the resolved value passed to a provider runtime. Value is
// provider-owned JSON for credentials such as OAuth; APIKey is kept separate
// so ordinary API-key providers do not need to decode an envelope.
type Credential struct {
	Type   AuthKind        `json:"type,omitempty"`
	APIKey string          `json:"apiKey,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}

// ExtensionProviderSpec is the declarative catalog and auth contract
// contributed by a provider-capable extension. The extension process owns the
// implementation.
type ExtensionProviderSpec struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	API          string      `json:"api"`
	BaseURL      string      `json:"baseUrl"`
	DefaultModel string      `json:"defaultModel,omitempty"`
	Auth         AuthSpec    `json:"auth,omitzero"`
	Models       []ModelSeed `json:"models"`
}

// CatalogVersion is the schema version of the embedded provider catalog.
const CatalogVersion = 2

// CostRates are USD prices per million tokens.
type CostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// CostTier applies rates after a cumulative input-token threshold.
type CostTier struct {
	InputTokensAbove int `json:"inputTokensAbove"`
	CostRates
}

// Cost contains base rates and optional volume tiers.
type Cost struct {
	CostRates
	Tiers []CostTier `json:"tiers,omitempty"`
}

// Compat contains only request-shape differences Ki implements.
type Compat struct {
	ThinkingFormat           string `json:"thinkingFormat,omitempty"`
	SupportsReasoningEffort  bool   `json:"supportsReasoningEffort,omitempty"`
	SupportsDeveloperRole    *bool  `json:"supportsDeveloperRole,omitempty"`
	MaxTokensField           string `json:"maxTokensField,omitempty"`
	RequiresReasoningContent bool   `json:"requiresReasoningContent,omitempty"`
	ForceAdaptiveThinking    bool   `json:"forceAdaptiveThinking,omitempty"`
}

// Model is a fully resolved selectable model.
type Model struct {
	Provider           string             `json:"provider"`
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	API                string             `json:"api"`
	BaseURL            string             `json:"baseUrl"`
	Enabled            bool               `json:"enabled"`
	Builtin            bool               `json:"builtin"`
	Customized         bool               `json:"customized,omitempty"`
	ContextWindow      int                `json:"contextWindow"`
	MaxTokens          int                `json:"maxTokens"`
	Input              []string           `json:"input"`
	ApplyPatchToolType string             `json:"applyPatchToolType,omitempty"`
	Reasoning          bool               `json:"reasoning"`
	ThinkingLevelMap   map[string]*string `json:"thinkingLevelMap,omitempty"`
	Cost               *Cost              `json:"cost"`
	Compat             Compat             `json:"compat,omitzero"`
}

// Provider describes a connection plus its resolved models.
type Provider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	API          string   `json:"api"`
	BaseURL      string   `json:"baseUrl"`
	Auth         AuthSpec `json:"auth,omitzero"`
	Enabled      bool     `json:"enabled"`
	Builtin      bool     `json:"builtin"`
	Customized   bool     `json:"customized,omitempty"`
	Runtime      string   `json:"runtime,omitempty"`
	EnvVars      []string `json:"-"`
	DefaultModel string   `json:"defaultModel"`
	Models       []Model  `json:"models"`
}

type catalogFile struct {
	Version     int            `json:"version"`
	GeneratedAt string         `json:"generatedAt"`
	Providers   []catalogEntry `json:"providers"`
}

type catalogEntry struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	API          string      `json:"api"`
	BaseURL      string      `json:"baseUrl"`
	EnvVars      []string    `json:"envVars"`
	DefaultModel string      `json:"defaultModel"`
	Models       []ModelSeed `json:"models"`
}

// ModelSeed is the on-disk model definition used by built-in and user catalogs.
type ModelSeed struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name,omitempty"`
	Enabled            *bool              `json:"enabled,omitempty"`
	API                string             `json:"api,omitempty"`
	BaseURL            string             `json:"baseUrl,omitempty"`
	ContextWindow      int                `json:"contextWindow,omitempty"`
	MaxTokens          int                `json:"maxTokens,omitempty"`
	Input              []string           `json:"input,omitempty"`
	ApplyPatchToolType string             `json:"applyPatchToolType,omitempty"`
	Reasoning          *bool              `json:"reasoning,omitempty"`
	ThinkingLevelMap   map[string]*string `json:"thinkingLevelMap,omitempty"`
	Cost               *Cost              `json:"cost"`
	Compat             Compat             `json:"compat,omitzero"`
}

// BuildExtensionProvider resolves a manifest provider into the same selectable
// model shape used by the built-in registry. It does not attach executable
// behavior; the provider-capable extension owns that runtime.
func BuildExtensionProvider(spec ExtensionProviderSpec) (Provider, error) {
	if err := ValidateExtensionProviderSpec(spec); err != nil {
		return Provider{}, err
	}
	auth := spec.Auth
	if auth.Type == "" {
		auth.Type = AuthAPIKey
	}
	models := make([]Model, 0, len(spec.Models))
	for _, seed := range spec.Models {
		models = append(models, resolveSeed(spec.ID, spec.API, spec.BaseURL, seed, false))
	}
	defaultModel := spec.DefaultModel
	if defaultModel == "" {
		defaultModel = models[0].ID
	}
	return Provider{
		ID: spec.ID, Name: spec.Name, API: spec.API, BaseURL: strings.TrimRight(spec.BaseURL, "/"),
		Auth: auth, Enabled: true, Models: models, DefaultModel: defaultModel, Runtime: "extension",
	}, nil
}

//go:embed catalog.json
var catalogFS embed.FS

var builtinCatalog = mustBuiltinCatalog()

func mustBuiltinCatalog() catalogFile {
	b, err := catalogFS.ReadFile("catalog.json")
	if err != nil {
		panic(err)
	}
	var c catalogFile
	if err := json.Unmarshal(b, &c); err != nil {
		panic(fmt.Errorf("decode embedded provider catalog: %w", err))
	}
	if c.Version != CatalogVersion {
		panic(fmt.Errorf("%w %d, want %d", errCatalogVersion, c.Version, CatalogVersion))
	}
	return c
}

// BuiltinProviders returns a fresh copy of the embedded provider catalog.
func BuiltinProviders() []Provider {
	out := make([]Provider, 0, len(builtinCatalog.Providers))
	for _, p := range builtinCatalog.Providers {
		models := make([]Model, 0, len(p.Models))
		for _, seed := range p.Models {
			models = append(models, resolveSeed(p.ID, p.API, p.BaseURL, seed, true))
		}
		out = append(out, Provider{
			ID: p.ID, Name: p.Name, API: p.API, BaseURL: p.BaseURL,
			Auth: AuthSpec{Type: AuthAPIKey}, Enabled: true, Builtin: true,
			EnvVars: slices.Clone(p.EnvVars), DefaultModel: p.DefaultModel, Models: models,
		})
	}
	aliases := []struct {
		source, id, name, base string
		env                    []string
	}{
		{"dashscope", "dashscope-cn", "DashScope CN", "https://dashscope.aliyuncs.com/compatible-mode/v1", []string{"DASHSCOPE_CN_API_KEY", "DASHSCOPE_API_KEY"}},
		{"zai", "zai-cn", "Z.AI CN", "https://open.bigmodel.cn/api/coding/paas/v4", []string{"ZAI_CODING_CN_API_KEY"}},
		{"moonshot", "moonshot-cn", "Moonshot AI CN", "https://api.moonshot.cn/v1", []string{"MOONSHOT_CN_API_KEY", "MOONSHOT_API_KEY"}},
		{"minimax", "minimax-cn", "MiniMax CN", "https://api.minimaxi.com/anthropic", []string{"MINIMAX_CN_API_KEY"}},
	}
	for _, a := range aliases {
		i := slices.IndexFunc(out, func(p Provider) bool { return p.ID == a.source })
		if i < 0 {
			panic("missing regional catalog source: " + a.source)
		}
		p := out[i]
		p.ID, p.Name, p.BaseURL, p.EnvVars = a.id, a.name, a.base, slices.Clone(a.env)
		p.Models = slices.Clone(p.Models)
		for i := range p.Models {
			p.Models[i].Provider, p.Models[i].BaseURL = p.ID, p.BaseURL
		}
		out = append(out, p)
	}
	return out
}

func resolveSeed(providerID, providerAPI, providerBase string, seed ModelSeed, builtin bool) Model {
	enabled := true
	if seed.Enabled != nil {
		enabled = *seed.Enabled
	}
	name := seed.Name
	if name == "" {
		name = seed.ID
	}
	api := seed.API
	if api == "" {
		api = providerAPI
	}
	base := strings.TrimRight(seed.BaseURL, "/")
	if base == "" {
		base = strings.TrimRight(providerBase, "/")
	}
	window := seed.ContextWindow
	if window == 0 {
		window = 128000
	}
	maxTokens := seed.MaxTokens
	if maxTokens == 0 {
		maxTokens = 16384
	}
	input := slices.Clone(seed.Input)
	if len(input) == 0 {
		input = []string{"text"}
	}
	reasoning := false
	if seed.Reasoning != nil {
		reasoning = *seed.Reasoning
	}
	return Model{Provider: providerID, ID: seed.ID, Name: name, API: api, BaseURL: base, Enabled: enabled, Builtin: builtin, Customized: !builtin, ContextWindow: window, MaxTokens: maxTokens, Input: input, ApplyPatchToolType: seed.ApplyPatchToolType, Reasoning: reasoning, ThinkingLevelMap: cloneThinkingMap(seed.ThinkingLevelMap), Cost: cloneCost(seed.Cost), Compat: seed.Compat}
}

func cloneThinkingMap(in map[string]*string) map[string]*string {
	if in == nil {
		return nil
	}
	out := make(map[string]*string, len(in))
	for k, v := range in {
		if v == nil {
			out[k] = nil
		} else {
			s := *v
			out[k] = &s
		}
	}
	return out
}

func cloneCost(in *Cost) *Cost {
	if in == nil {
		return nil
	}
	out := *in
	out.Tiers = slices.Clone(in.Tiers)
	return &out
}

// ProviderOrder defines the stable presentation order for provider IDs.
var ProviderOrder = []string{"anthropic", "openai", "deepseek", "dashscope-cn", "dashscope", "zai-cn", "zai", "moonshot-cn", "moonshot", "minimax-cn", "minimax", "google", "xai"}
