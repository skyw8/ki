package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinCatalogHasSupportedProviders(t *testing.T) {
	got := map[string]bool{}
	for _, p := range BuiltinProviders() {
		got[p.ID] = true
	}
	for _, id := range []string{"openai", "anthropic", "deepseek", "dashscope", "dashscope-cn", "zai", "zai-cn", "moonshot", "moonshot-cn", "minimax", "minimax-cn", "google", "xai"} {
		if !got[id] {
			t.Errorf("missing provider %q", id)
		}
	}
}

func TestBuiltinGPTModelsAdvertiseFreeformApplyPatch(t *testing.T) {
	for _, p := range BuiltinProviders() {
		if p.ID != "openai" {
			continue
		}
		for _, model := range p.Models {
			if model.ApplyPatchToolType != "freeform" || model.API != "responses" {
				t.Fatalf("OpenAI model %+v does not advertise Responses freeform apply_patch", model)
			}
		}
		return
	}
	t.Fatal("openai provider missing")
}

func TestRegistryRejectsFreeformApplyPatchOutsideResponses(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	on := true
	reasoning := false
	err = r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["bad"] = Config{
			Name: "Bad", API: "completions", BaseURL: "http://127.0.0.1/v1", Enabled: &on,
			Models: []ModelSeed{{ID: "bad", Input: []string{"text"}, ApplyPatchToolType: "freeform", Reasoning: &reasoning}},
		}
		return nil
	})
	if err == nil {
		t.Fatal("freeform apply_patch on completions model must fail")
	}
}

func TestRegistryPersistsCustomProviderAndCredential(t *testing.T) {
	home := t.TempDir()
	r, err := NewRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	reasoning := false
	err = r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["local"] = Config{
			Name: "Local", API: "completions", BaseURL: "http://127.0.0.1:11434/v1", Enabled: &on,
			Models: []ModelSeed{{ID: "example/model", ContextWindow: 8192, MaxTokens: 1024, Input: []string{"text"}, Reasoning: &reasoning}},
		}
		cfg.Default = ModelRef{Provider: "local", Model: "example/model"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "secret"
	if err := r.SetCredential("local", &key); err != nil {
		t.Fatal(err)
	}
	r, err = NewRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	_, model, gotKey, err := r.Resolve("local", "example/model")
	if err != nil || gotKey != key || model.ContextWindow != 8192 || model.Builtin {
		t.Fatalf("resolve = model %+v key %q err %v", model, gotKey, err)
	}
	if got := r.Default(); got.Provider != "local" || got.Model != "example/model" {
		t.Fatalf("default = %+v", got)
	}
	if info, err := os.Stat(filepath.Join(home, "credentials.json")); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("credential permissions: info=%v err=%v", info, err)
	}
}

func TestRegistryFallsBackWhenLastUsedUnavailable(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RememberDefault(ModelRef{Provider: "openai", Model: "gpt-5.6-terra"}); err != nil {
		t.Fatal(err)
	}
	if got := r.Default(); got.Provider != "openai" || got.Model != "gpt-5.6-terra" {
		t.Fatalf("last used = %+v", got)
	}
	off := false
	if err := r.Update(func(cfg *ModelsFile) error {
		pc := cfg.Providers["openai"]
		pc.Enabled = &off
		cfg.Providers["openai"] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := r.Default()
	if got.Provider == "openai" || got.Provider == "" || got.Model == "" {
		t.Fatalf("must fall back from disabled last-used: %+v", got)
	}
}

func TestDefaultThinkingPrefersMediumAndClampPicksNearest(t *testing.T) {
	reasoning := Model{Reasoning: true}
	if got := DefaultThinking(reasoning); got != "medium" {
		t.Fatalf("default reasoning = %q", got)
	}
	if got := DefaultThinking(Model{}); got != "off" {
		t.Fatalf("default non-reasoning = %q", got)
	}
	skipMedium := Model{Reasoning: true, ThinkingLevelMap: map[string]*string{"medium": nil, "high": ptrLevel("high")}}
	if got := DefaultThinking(skipMedium); got != "off" {
		t.Fatalf("default skipped medium = %q", got)
	}
	got, err := ClampThinking(reasoning, "high")
	if err != nil || got != "high" {
		t.Fatalf("clamp high = %q %v", got, err)
	}
	got, err = ClampThinking(reasoning, "max")
	if err != nil || got != "high" {
		t.Fatalf("clamp implicit max = %q %v", got, err)
	}
	withMax := Model{Reasoning: true, ThinkingLevelMap: map[string]*string{"max": ptrLevel("max")}}
	got, err = ClampThinking(withMax, "max")
	if err != nil || got != "max" {
		t.Fatalf("clamp mapped max = %q %v", got, err)
	}
	noMax := Model{Reasoning: true, ThinkingLevelMap: map[string]*string{"xhigh": nil, "max": nil}}
	got, err = ClampThinking(noMax, "max")
	if err != nil || got != "high" {
		t.Fatalf("clamp down = %q %v", got, err)
	}
	got, err = ClampThinking(skipMedium, "")
	if err != nil || got != "" {
		t.Fatalf("clamp empty = %q %v", got, err)
	}
}

func ptrLevel(s string) *string { return &s }

func TestRegistryStrictJSONRejectsTrailingValue(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(`{"version":1,"providers":{}} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(home); err == nil {
		t.Fatal("trailing JSON must fail")
	}
}
