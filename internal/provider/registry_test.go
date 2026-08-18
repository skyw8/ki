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

func TestRegistryPersistsCustomProviderAndCredential(t *testing.T) {
	home := t.TempDir()
	r, err := NewRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	reasoning := false
	err = r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["local"] = ProviderConfig{
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

func TestRegistryRejectsDisablingDefault(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		cfg.Default = ModelRef{Provider: "openai", Model: "gpt-5.6-terra"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	off := false
	err = r.Update(func(cfg *ModelsFile) error {
		pc := cfg.Providers["openai"]
		pc.Enabled = &off
		cfg.Providers["openai"] = pc
		return nil
	})
	if err == nil {
		t.Fatal("disabling the default provider must fail")
	}
}

func TestRegistryStrictJSONRejectsTrailingValue(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(`{"version":1,"providers":{}} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(home); err == nil {
		t.Fatal("trailing JSON must fail")
	}
}
