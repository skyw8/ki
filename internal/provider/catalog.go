package provider

import (
	"cmp"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func CredentialFingerprint(credential Credential) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(credential.Type))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(credential.APIKey))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(credential.Value)
	return fmt.Sprintf("%x", hash.Sum(nil)[:16])
}

type AuthKind string

const (
	AuthNone   AuthKind = "none"
	AuthAPIKey AuthKind = "api_key"
	AuthOAuth  AuthKind = "oauth"
)

type AuthSpec struct {
	Type         AuthKind `json:"type,omitempty"`
	Name         string   `json:"name,omitempty"`
	Subscription bool     `json:"subscription,omitzero"`
}

type Credential struct {
	Type   AuthKind        `json:"type,omitempty"`
	APIKey string          `json:"apiKey,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}

type ExtensionProviderSpec struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	API          string      `json:"api"`
	BaseURL      string      `json:"baseUrl"`
	DefaultModel string      `json:"defaultModel,omitempty"`
	Auth         AuthSpec    `json:"auth,omitzero"`
	Models       []ModelSeed `json:"models"`
}

const CatalogVersion = 4

type CostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type CostTier struct {
	InputTokensAbove int `json:"inputTokensAbove"`
	CostRates
}

type Cost struct {
	CostRates
	Tiers []CostTier `json:"tiers,omitempty"`
}

type Compat struct {
	ThinkingFormat           string `json:"thinkingFormat,omitempty"`
	SupportsReasoningEffort  bool   `json:"supportsReasoningEffort,omitzero"`
	SupportsDeveloperRole    *bool  `json:"supportsDeveloperRole,omitempty"`
	MaxTokensField           string `json:"maxTokensField,omitempty"`
	RequiresReasoningContent bool   `json:"requiresReasoningContent,omitzero"`
	ForceAdaptiveThinking    bool   `json:"forceAdaptiveThinking,omitzero"`
}

type CompactionCapabilities struct {
	Standalone string `json:"standalone,omitempty"`
	Inline     string `json:"inline,omitempty"`
}

type Model struct {
	Provider           string                 `json:"provider"`
	ID                 string                 `json:"id"`
	Name               string                 `json:"name"`
	API                string                 `json:"api"`
	BaseURL            string                 `json:"baseUrl"`
	Enabled            bool                   `json:"enabled"`
	Builtin            bool                   `json:"builtin"`
	Customized         bool                   `json:"customized,omitzero"`
	ContextWindow      int                    `json:"contextWindow"`
	MaxTokens          int                    `json:"maxTokens"`
	Input              []string               `json:"input"`
	ApplyPatchToolType string                 `json:"applyPatchToolType,omitempty"`
	ExecToolType       string                 `json:"execToolType,omitempty"`
	Compaction         CompactionCapabilities `json:"compaction,omitzero"`
	Reasoning          bool                   `json:"reasoning"`
	ThinkingLevelMap   map[string]*string     `json:"thinkingLevelMap,omitempty"`
	FastServiceTier    string                 `json:"fastServiceTier,omitempty"`
	Cost               *Cost                  `json:"cost"`
	Compat             Compat                 `json:"compat,omitzero"`
}

func (m Model) SupportsFreeformExec() bool {
	return m.supportsFreeformTool(m.ExecToolType)
}

func (m Model) SupportsFreeformApplyPatch() bool {
	return m.supportsFreeformTool(m.ApplyPatchToolType)
}

func (m Model) supportsFreeformTool(toolType string) bool {
	return toolType == "freeform" && m.API != "completions" && m.API != "anthropic"
}

type Provider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	API          string   `json:"api"`
	BaseURL      string   `json:"baseUrl"`
	Auth         AuthSpec `json:"auth,omitzero"`
	Enabled      bool     `json:"enabled"`
	Builtin      bool     `json:"builtin"`
	Customized   bool     `json:"customized,omitzero"`
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

type ModelSeed struct {
	ID                 string                 `json:"id"`
	Name               string                 `json:"name,omitempty"`
	Enabled            *bool                  `json:"enabled,omitempty"`
	API                string                 `json:"api,omitempty"`
	BaseURL            string                 `json:"baseUrl,omitempty"`
	ContextWindow      int                    `json:"contextWindow,omitzero"`
	MaxTokens          int                    `json:"maxTokens,omitzero"`
	Input              []string               `json:"input,omitempty"`
	ApplyPatchToolType string                 `json:"applyPatchToolType,omitempty"`
	ExecToolType       string                 `json:"execToolType,omitempty"`
	Compaction         CompactionCapabilities `json:"compaction,omitzero"`
	Reasoning          *bool                  `json:"reasoning,omitempty"`
	ThinkingLevelMap   map[string]*string     `json:"thinkingLevelMap,omitempty"`
	FastServiceTier    string                 `json:"fastServiceTier,omitempty"`
	Cost               *Cost                  `json:"cost"`
	Compat             Compat                 `json:"compat,omitzero"`
}

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
	defaultModel := firstEnabledExtensionModel(models, spec.DefaultModel)
	return Provider{
		ID: spec.ID, Name: spec.Name, API: spec.API, BaseURL: strings.TrimRight(spec.BaseURL, "/"),
		Auth: auth, Enabled: true, Models: models, DefaultModel: defaultModel, Runtime: "extension",
	}, nil
}

func firstEnabledExtensionModel(models []Model, preferred string) string {
	if preferred != "" {
		for _, model := range models {
			if model.ID == preferred && model.Enabled {
				return model.ID
			}
		}
	}
	for _, model := range models {
		if model.Enabled {
			return model.ID
		}
	}
	return ""
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
	name := cmp.Or(seed.Name, seed.ID)
	api := cmp.Or(seed.API, providerAPI)
	base := cmp.Or(strings.TrimRight(seed.BaseURL, "/"), strings.TrimRight(providerBase, "/"))
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
	return Model{Provider: providerID, ID: seed.ID, Name: name, API: api, BaseURL: base, Enabled: enabled, Builtin: builtin, Customized: !builtin, ContextWindow: window, MaxTokens: maxTokens, Input: input, ApplyPatchToolType: seed.ApplyPatchToolType, ExecToolType: seed.ExecToolType, Compaction: seed.Compaction, Reasoning: reasoning, ThinkingLevelMap: cloneThinkingMap(seed.ThinkingLevelMap), FastServiceTier: seed.FastServiceTier, Cost: cloneCost(seed.Cost), Compat: seed.Compat}
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

var ProviderOrder = []string{"openrouter", "anthropic", "openai", "deepseek", "dashscope-cn", "dashscope", "zai-cn", "zai", "moonshot-cn", "moonshot", "minimax-cn", "minimax", "google", "xai", "magpie"}
