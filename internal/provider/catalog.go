package provider

// Model describes a selectable model.
type Model struct {
	Provider      string
	ID            string
	API           string // completions | responses | anthropic
	ContextWindow int
	BaseURL       string
}

// DefaultModel is the preferred model id per provider.
var DefaultModel = map[string]string{
	"openai":       "gpt-4o",
	"anthropic":    "claude-sonnet-4-5",
	"zhipu":        "glm-4.5",
	"zhipu-cn":     "glm-4.5",
	"deepseek":     "deepseek-v4-flash",
	"dashscope":    "qwen-plus",
	"dashscope-cn": "qwen-plus",
}

// Catalog is the built-in provider table.
func Catalog() []Model {
	return []Model{
		{Provider: "openai", ID: "gpt-4o", API: "responses", ContextWindow: 128000, BaseURL: "https://api.openai.com/v1"},
		{Provider: "openai", ID: "gpt-4o-mini", API: "responses", ContextWindow: 128000, BaseURL: "https://api.openai.com/v1"},
		{Provider: "openai", ID: "gpt-4.1", API: "responses", ContextWindow: 1047576, BaseURL: "https://api.openai.com/v1"},
		{Provider: "anthropic", ID: "claude-sonnet-4-5", API: "anthropic", ContextWindow: 200000, BaseURL: "https://api.anthropic.com"},
		{Provider: "anthropic", ID: "claude-opus-4", API: "anthropic", ContextWindow: 200000, BaseURL: "https://api.anthropic.com"},
		{Provider: "anthropic", ID: "claude-haiku-4-5", API: "anthropic", ContextWindow: 200000, BaseURL: "https://api.anthropic.com"},
		{Provider: "zhipu", ID: "glm-4.5", API: "completions", ContextWindow: 128000, BaseURL: "https://api.z.ai/api/coding/paas/v4"},
		{Provider: "zhipu", ID: "glm-4-plus", API: "completions", ContextWindow: 128000, BaseURL: "https://api.z.ai/api/coding/paas/v4"},
		{Provider: "zhipu-cn", ID: "glm-4.5", API: "completions", ContextWindow: 128000, BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4"},
		{Provider: "zhipu-cn", ID: "glm-4-plus", API: "completions", ContextWindow: 128000, BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4"},
		{Provider: "deepseek", ID: "deepseek-v4-flash", API: "completions", ContextWindow: 1000000, BaseURL: "https://api.deepseek.com"},
		{Provider: "deepseek", ID: "deepseek-v4-pro", API: "completions", ContextWindow: 1000000, BaseURL: "https://api.deepseek.com"},
		{Provider: "deepseek", ID: "deepseek-chat", API: "completions", ContextWindow: 128000, BaseURL: "https://api.deepseek.com"},
		{Provider: "deepseek", ID: "deepseek-reasoner", API: "completions", ContextWindow: 128000, BaseURL: "https://api.deepseek.com"},
		{Provider: "dashscope", ID: "qwen-plus", API: "completions", ContextWindow: 131072, BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
		{Provider: "dashscope", ID: "qwen-max", API: "completions", ContextWindow: 32768, BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
		{Provider: "dashscope-cn", ID: "qwen-plus", API: "completions", ContextWindow: 131072, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		{Provider: "dashscope-cn", ID: "qwen-max", API: "completions", ContextWindow: 32768, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	}
}

// Lookup finds a model; unknown ids still resolve API/baseURL from provider.
func Lookup(provider, id string) Model {
	for _, m := range Catalog() {
		if m.Provider == provider && m.ID == id {
			return m
		}
	}
	for _, m := range Catalog() {
		if m.Provider == provider {
			m.ID = id
			return m
		}
	}
	return Model{Provider: provider, ID: id, API: "completions", ContextWindow: 128000}
}

// ProviderOrder is used when picking the first configured default.
var ProviderOrder = []string{"anthropic", "openai", "zhipu-cn", "zhipu", "deepseek", "dashscope-cn", "dashscope"}
