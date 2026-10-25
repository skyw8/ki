package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinCatalogHasSupportedProviders(t *testing.T) {
	got := map[string]bool{}
	for _, p := range BuiltinProviders() {
		got[p.ID] = true
	}
	for _, id := range []string{"openrouter", "openai", "anthropic", "deepseek", "dashscope", "dashscope-cn", "zai", "zai-cn", "moonshot", "moonshot-cn", "minimax", "minimax-cn", "google", "xai"} {
		if !got[id] {
			t.Errorf("missing provider %q", id)
		}
	}
}

func TestBuiltinOpenRouterFreeModelIsFirstDefault(t *testing.T) {
	providers := BuiltinProviders()
	if len(providers) == 0 || providers[0].ID != "openrouter" {
		t.Fatalf("first builtin provider = %+v, want openrouter", providers)
	}
	if len(providers[0].Models) != 1 || providers[0].Models[0].ID != "free" {
		t.Fatalf("openrouter models = %+v, want only free", providers[0].Models)
	}
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Default(); got.Provider != "openrouter" || got.Model != "free" {
		t.Fatalf("default = %+v, want openrouter/free", got)
	}
}

func TestBuiltinDeepSeekFlashAdvertisesImageInput(t *testing.T) {
	for _, p := range BuiltinProviders() {
		if p.ID != "deepseek" {
			continue
		}
		if len(p.Models) != 1 || p.Models[0].ID != "deepseek-flash" {
			t.Fatalf("DeepSeek models = %+v, want only deepseek-flash", p.Models)
		}
		model := p.Models[0]
		if !slices.Contains(model.Input, "image") {
			t.Fatalf("DeepSeek deepseek-flash input = %v, want image", model.Input)
		}
		if model.API != "completions" || model.BaseURL != "https://api.deepseek.com" {
			t.Fatalf("DeepSeek deepseek-flash endpoint = %s %s", model.API, model.BaseURL)
		}
		if p.DefaultModel != "deepseek-flash" {
			t.Fatalf("DeepSeek default = %q, want deepseek-flash", p.DefaultModel)
		}
		return
	}
	t.Fatal("deepseek provider missing")
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
	model, err = r.ResolveRef(ModelRef{Provider: "local", Model: "example/model"})
	if err != nil || model.ID != "example/model" {
		t.Fatalf("resolve structured ref = model %+v err %v", model, err)
	}
	if got := r.Default(); got.Provider != "local" || got.Model != "example/model" {
		t.Fatalf("default = %+v", got)
	}
	if info, err := os.Stat(filepath.Join(home, "credentials.json")); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		t.Fatalf("credential permissions: info=%v err=%v", info, err)
	}
}

func TestRegistryRegistersExtensionProviderAndOpaqueCredential(t *testing.T) {
	home := t.TempDir()
	r, err := NewRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	spec := ExtensionProviderSpec{
		ID: "codex", Name: "Codex", API: "openai-codex-responses", BaseURL: "https://chatgpt.com/backend-api",
		DefaultModel: "codex-mini", Auth: AuthSpec{Type: AuthOAuth, Name: "Codex", Subscription: true},
		Models: []ModelSeed{{ID: "codex-mini", ContextWindow: 128000, MaxTokens: 16384, Input: []string{"text"}, ApplyPatchToolType: "freeform", ExecToolType: "freeform"}},
	}
	if err := r.ReplaceExtensionProviders([]ExtensionProviderSpec{spec}); err != nil {
		t.Fatal(err)
	}
	views := r.Providers()
	var found *View
	for i := range views {
		if views[i].ID == spec.ID {
			found = &views[i]
			break
		}
	}
	if found == nil || found.Runtime != "extension" || found.Auth.Type != AuthOAuth || found.Credential.Configured {
		t.Fatalf("extension view=%+v", found)
	}
	if len(found.Models) != 1 || found.Models[0].ApplyPatchToolType != "freeform" || found.Models[0].ExecToolType != "freeform" {
		t.Fatalf("extension model capability=%+v", found.Models)
	}
	if _, _, _, err := r.Resolve(spec.ID, "codex-mini"); !strings.Contains(err.Error(), "configured credential") {
		t.Fatalf("missing opaque credential error=%v", err)
	}
	value := json.RawMessage(`{"access":"access-token","refresh":"refresh-token","accountId":"acct"}`)
	if err := r.SetCredentialValue(spec.ID, AuthOAuth, value); err != nil {
		t.Fatal(err)
	}
	if err := r.SetCredentialValue(spec.ID, AuthAPIKey, value); err == nil {
		t.Fatal("wrong credential type must fail")
	}
	credential, status, err := r.Credential(spec.ID)
	if err != nil || !status.Configured || status.Type != AuthOAuth || string(credential.Value) != string(value) || credential.APIKey != "" {
		t.Fatalf("credential=%+v status=%+v err=%v", credential, status, err)
	}
	if _, _, _, err := r.Resolve(spec.ID, "codex-mini"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "credentials.json")) //nolint:gosec // path is under the test's temporary home
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "access-token") || !strings.Contains(string(b), "refresh-token") {
		t.Fatalf("opaque credential was not persisted: %s", b)
	}
	catalog, err := json.Marshal(r.Providers())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(catalog), "access-token") || strings.Contains(string(catalog), "refresh-token") {
		t.Fatalf("provider catalog leaked opaque credential: %s", catalog)
	}
}

func TestExtensionProviderFallsBackToFirstEnabledModel(t *testing.T) {
	disabled := false
	p, err := BuildExtensionProvider(ExtensionProviderSpec{
		ID: "codex", Name: "Codex", API: "openai-codex-responses", BaseURL: "https://chatgpt.com/backend-api",
		DefaultModel: "deleted-model", Auth: AuthSpec{Type: AuthOAuth},
		Models: []ModelSeed{
			{ID: "disabled-model", Enabled: &disabled},
			{ID: "first-available"},
			{ID: "later-model"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.DefaultModel != "first-available" {
		t.Fatalf("default model = %q, want first enabled model", p.DefaultModel)
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

func TestThinkingLevelsCanHideOffAndMapMinimal(t *testing.T) {
	luna := Model{
		Reasoning: true,
		ThinkingLevelMap: map[string]*string{
			"off":     nil,
			"minimal": ptrLevel("low"),
			"xhigh":   ptrLevel("xhigh"),
			"max":     ptrLevel("max"),
		},
	}
	want := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	if got := SupportedThinkingLevels(luna); !slices.Equal(got, want) {
		t.Fatalf("supported Luna thinking levels = %v, want %v", got, want)
	}
	if got, err := ClampThinking(luna, "off"); err != nil || got != "minimal" {
		t.Fatalf("clamp hidden off = %q %v, want minimal", got, err)
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

func TestRegistryMigratesVersion1RemoteCompaction(t *testing.T) {
	home := t.TempDir()
	doc := `{
  "version": 1,
  "default": {"provider": "openrouter", "model": "free"},
  "providers": {
    "openrouter": {
      "modelOverrides": {
        "free": {"api": "responses", "remoteCompaction": "openai"}
      }
    },
    "custom": {
      "name": "Custom",
      "api": "responses",
      "baseUrl": "https://example.test/v1",
      "models": [
        {"id": "m", "name": "M", "contextWindow": 1000, "maxTokens": 100, "input": ["text"], "remoteCompaction": "openai"},
        {"id": "m-off", "name": "M Off", "contextWindow": 1000, "maxTokens": 100, "input": ["text"], "remoteCompaction": ""}
      ]
    }
  }
}`
	path := filepath.Join(home, "models.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(home)
	if err != nil {
		t.Fatalf("version 1 models.json must migrate, got %v", err)
	}
	want := CompactionCapabilities{Standalone: "openai", Inline: "openai"}
	for _, ref := range []ModelRef{
		{Provider: "openrouter", Model: "free"},
		{Provider: "custom", Model: "m"},
	} {
		_, m, ok := r.FindModel(ref.Provider, ref.Model)
		if !ok {
			t.Fatalf("model %s/%s missing after migration", ref.Provider, ref.Model)
		}
		if m.Compaction != want {
			t.Fatalf("model %s/%s compaction = %+v, want %+v", ref.Provider, ref.Model, m.Compaction, want)
		}
	}
	if _, m, ok := r.FindModel("custom", "m-off"); !ok || m.Compaction != (CompactionCapabilities{}) {
		t.Fatalf("empty remoteCompaction compaction = %+v, want none", m.Compaction)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), `"version": 1`) {
		t.Fatalf("load rewrote models.json: %s", onDisk)
	}
}

func TestRegistryRejectsNewerModelsFileVersion(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(`{"version":99,"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(home); !errors.Is(err, errUnsupportedVersion) {
		t.Fatalf("newer version error = %v, want %v", err, errUnsupportedVersion)
	}
}

func TestRegistryRejectsNewerCredentialsFileVersion(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "credentials.json"), []byte(`{"version":99,"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(home); !errors.Is(err, errUnsupportedVersion) {
		t.Fatalf("newer credentials version error = %v, want %v", err, errUnsupportedVersion)
	}
}

func TestRegistryMigratedVersionKeepsStrictFields(t *testing.T) {
	home := t.TempDir()
	doc := `{"version":1,"providers":{"openai":{"bogus":true}}}`
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(home); err == nil {
		t.Fatal("unknown field must still fail after migration")
	}
}

func syncedRegistryModel(t *testing.T, r *Registry, providerID, modelID string) Model {
	t.Helper()
	for _, p := range r.Providers() {
		if p.ID == providerID {
			for _, model := range p.Models {
				if model.ID == modelID {
					return model
				}
			}
		}
	}
	t.Fatalf("missing model %s/%s", providerID, modelID)
	return Model{}
}

func TestSyncModelsPreservesManualModelsAndPrunesOnlySyncedOrphans(t *testing.T) {
	for _, id := range []string{"local", "openrouter"} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			r, err := NewRegistry(home)
			if err != nil {
				t.Fatal(err)
			}
			manual := ModelSeed{ID: "collision", Name: "Manual model", Enabled: modelMetadataPointer(false), ContextWindow: 120000, MaxTokens: 16000, Input: []string{"text", "image"}, Reasoning: modelMetadataPointer(true), ThinkingLevelMap: map[string]*string{"high": modelMetadataPointer("high")}, Cost: &Cost{CostRates: CostRates{Input: 2, Output: 8}}, Compat: Compat{SupportsReasoningEffort: true}}
			if err := r.Update(func(cfg *ModelsFile) error {
				pc := Config{
					Name: "Catalog", API: "completions", BaseURL: "https://models.example/v1",
					Models:       []ModelSeed{manual, {ID: "manual-only"}, {ID: "retired"}, {ID: "kept"}},
					SyncedModels: []string{"retired", "kept"},
					ModelOverrides: map[string]ModelOverride{
						"collision":   {MaxTokens: modelMetadataPointer(14000)},
						"manual-only": {Name: modelMetadataPointer("Manual override")},
						"retired":     {Enabled: modelMetadataPointer(false)},
						"kept":        {Enabled: modelMetadataPointer(false)},
					},
				}
				if id == "openrouter" {
					pc.ModelOverrides["free"] = ModelOverride{Name: modelMetadataPointer("Builtin override"), Enabled: modelMetadataPointer(false)}
				}
				cfg.Providers[id] = pc
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			key := "secret"
			if err := r.SetCredential(id, &key); err != nil {
				t.Fatal(err)
			}
			before := syncedRegistryModel(t, r, id, "collision")
			incoming := []ModelSeed{{ID: "collision", Name: "Upstream collision", ContextWindow: 32000}, {ID: "kept", Name: "Updated"}, {ID: "new"}}
			if err := r.SyncModelsIfCurrent(id, "completions", "https://models.example/v1", key, incoming); err != nil {
				t.Fatal(err)
			}
			pc := r.UserConfig().Providers[id]
			if !reflect.DeepEqual(pc.Models[0], manual) || !reflect.DeepEqual(pc.SyncedModels, []string{"kept", "new"}) {
				t.Fatalf("manual ownership lost: %+v", pc)
			}
			if _, exists := pc.ModelOverrides["retired"]; exists {
				t.Fatal("retired synced override retained")
			}
			for _, modelID := range []string{"collision", "manual-only", "kept"} {
				if _, exists := pc.ModelOverrides[modelID]; !exists {
					t.Fatalf("override %q was removed", modelID)
				}
			}
			after := syncedRegistryModel(t, r, id, "collision")
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("manual collision changed:\ngot %+v\nwant %+v", after, before)
			}
			if id == "openrouter" {
				builtin := syncedRegistryModel(t, r, id, "free")
				if builtin.Name != "Builtin override" || builtin.Enabled {
					t.Fatalf("unsynced builtin override lost: %+v", builtin)
				}
			}
			if err := r.SyncModelsIfCurrent(id, "completions", "https://models.example/v1", key, nil); err != nil {
				t.Fatal(err)
			}
			pc = r.UserConfig().Providers[id]
			wantOverrides := 2
			if id == "openrouter" {
				wantOverrides++
			}
			if len(pc.Models) != 2 || len(pc.SyncedModels) != 0 || len(pc.ModelOverrides) != wantOverrides {
				t.Fatalf("empty refresh removed manual metadata: %+v", pc)
			}
			if _, err := NewRegistry(home); err != nil {
				t.Fatalf("synced catalog failed reload: %v", err)
			}
		})
	}
}

func TestSyncModelsPreservesInheritedAndPerModelEndpoints(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["local"] = Config{
			Name: "Local", API: "completions", BaseURL: "https://models.example/v1",
			Models:         []ModelSeed{{ID: "inherited"}, {ID: "custom", API: "responses", BaseURL: "https://custom.example/v1"}},
			SyncedModels:   []string{"inherited", "custom"},
			ModelOverrides: map[string]ModelOverride{"custom": {Enabled: modelMetadataPointer(false)}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := "secret"
	if err := r.SetCredential("local", &key); err != nil {
		t.Fatal(err)
	}
	p, _, _, err := r.ProviderCredential("local")
	if err != nil {
		t.Fatal(err)
	}
	models, err := ParseProviderModels([]byte(`{"data":[{"id":"inherited"},{"id":"custom"}]}`), p.Models)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SyncModelsIfCurrent(p.ID, p.API, p.BaseURL, key, models); err != nil {
		t.Fatal(err)
	}
	seeds := r.UserConfig().Providers[p.ID].Models
	if seeds[0].API != "" || seeds[0].BaseURL != "" || seeds[1].API != "responses" || seeds[1].BaseURL != "https://custom.example/v1" {
		t.Fatalf("endpoint inheritance = %+v", seeds)
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		pc := cfg.Providers[p.ID]
		pc.API, pc.BaseURL = "anthropic", "https://changed.example/v1"
		cfg.Providers[p.ID] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	inherited := syncedRegistryModel(t, r, p.ID, "inherited")
	custom := syncedRegistryModel(t, r, p.ID, "custom")
	if inherited.API != "anthropic" || inherited.BaseURL != "https://changed.example/v1" || custom.API != "responses" || custom.BaseURL != "https://custom.example/v1" || custom.Enabled {
		t.Fatalf("endpoint update = inherited %+v custom %+v", inherited, custom)
	}
}

func TestSyncModelsPreservesBuiltinOverrideAfterSyncedShadowLeaves(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["openrouter"] = Config{
			Models: []ModelSeed{{ID: "free", Name: "Synced shadow"}}, SyncedModels: []string{"free"},
			ModelOverrides: map[string]ModelOverride{"free": {Name: modelMetadataPointer("Keep override")}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := "secret"
	if err := r.SetCredential("openrouter", &key); err != nil {
		t.Fatal(err)
	}
	p, _, _, err := r.ProviderCredential("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SyncModelsIfCurrent(p.ID, p.API, p.BaseURL, key, []ModelSeed{{ID: "free", Name: "Remote shadow"}}); err != nil {
		t.Fatal(err)
	}
	pc := r.UserConfig().Providers[p.ID]
	if len(pc.Models) != 0 || len(pc.SyncedModels) != 0 {
		t.Fatalf("model adopted by builtin still tracked as synced: %+v", pc)
	}
	_, model, ok := r.FindModel("openrouter", "free")
	if !ok || !model.Builtin || model.Name != "Keep override" {
		t.Fatalf("builtin fallback override = %+v", model)
	}
}

func TestModelCatalogSnapshotDoesNotBakeOverridesIntoSyncedSeeds(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	baseline := ModelSeed{
		ID: "discovered", Name: "Baseline", ContextWindow: 100000, MaxTokens: 12000,
		Input: []string{"text", "image"}, Reasoning: modelMetadataPointer(true),
		ThinkingLevelMap: map[string]*string{"high": modelMetadataPointer("high")},
		Cost:             &Cost{CostRates: CostRates{Input: 2, Output: 8}}, Compat: Compat{SupportsReasoningEffort: true},
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["openrouter"] = Config{
			Models: []ModelSeed{baseline}, SyncedModels: []string{baseline.ID},
			ModelOverrides: map[string]ModelOverride{
				baseline.ID: {Name: modelMetadataPointer("Old override"), Enabled: modelMetadataPointer(false), Input: modelMetadataPointer([]string{"text"}), Reasoning: modelMetadataPointer(false), Cost: json.RawMessage(`null`)},
				"free":      {Name: modelMetadataPointer("Builtin local name"), Enabled: modelMetadataPointer(false)},
			},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := "secret"
	if err := r.SetCredential("openrouter", &key); err != nil {
		t.Fatal(err)
	}
	current, credential, status, err := r.ProviderModelCatalogCredential("openrouter")
	if err != nil || credential.APIKey != key || !status.Configured {
		t.Fatalf("catalog snapshot credential = %+v %+v %v", credential, status, err)
	}
	var existing Model
	for _, model := range current.Models {
		if model.ID == baseline.ID {
			existing = model
		}
	}
	if existing.Name != baseline.Name || !existing.Enabled || !existing.Reasoning || !slices.Contains(existing.Input, "image") || existing.Cost == nil || existing.Cost.Input != 2 {
		t.Fatalf("snapshot included local overrides: %+v", existing)
	}
	seeds, err := ParseProviderModels([]byte(`{"data":[{"id":"free"},{"id":"discovered"}]}`), current.Models)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		pc := cfg.Providers[current.ID]
		pc.ModelOverrides[baseline.ID] = ModelOverride{Name: modelMetadataPointer("Concurrent override")}
		cfg.Providers[current.ID] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncModelsIfCurrent(current.ID, current.API, current.BaseURL, key, seeds); err != nil {
		t.Fatal(err)
	}
	model := syncedRegistryModel(t, r, current.ID, baseline.ID)
	if model.Name != "Concurrent override" || !model.Enabled || !model.Reasoning || !slices.Contains(model.Input, "image") || model.Cost == nil || model.Cost.Input != 2 {
		t.Fatalf("concurrent override replacement retained stale fields: %+v", model)
	}
	builtin := syncedRegistryModel(t, r, current.ID, "free")
	if !builtin.Builtin || builtin.Name != "Builtin local name" || builtin.Enabled {
		t.Fatalf("builtin restore identity lost: %+v", builtin)
	}
	pc := r.UserConfig().Providers[current.ID]
	if len(pc.Models) != 1 || !reflect.DeepEqual(pc.SyncedModels, []string{baseline.ID}) {
		t.Fatalf("builtin model was imported as synced: %+v", pc)
	}
	current, _, _, err = r.ProviderModelCatalogCredential("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	seeds, err = ParseProviderModels([]byte(`{"data":[{"id":"free"},{"id":"discovered"}]}`), current.Models)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		pc := cfg.Providers[current.ID]
		pc.ModelOverrides = nil
		cfg.Providers[current.ID] = pc
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncModelsIfCurrent(current.ID, current.API, current.BaseURL, key, seeds); err != nil {
		t.Fatal(err)
	}
	model = syncedRegistryModel(t, r, current.ID, baseline.ID)
	if model.Name != baseline.Name || !model.Enabled || !model.Reasoning || !slices.Contains(model.Input, "image") || model.Cost == nil || model.Cost.Input != 2 {
		t.Fatalf("deleted override reappeared from snapshot: %+v", model)
	}
	builtin = syncedRegistryModel(t, r, current.ID, "free")
	if !builtin.Builtin || !builtin.Enabled || builtin.Name == "Builtin local name" {
		t.Fatalf("builtin restore did not recover embedded metadata: %+v", builtin)
	}
}

func TestSyncModelsRejectsStaleConfigurationAndInvalidCatalogs(t *testing.T) {
	for _, change := range []string{"api", "base", "key", "extension", "empty", "duplicate", "blank"} {
		t.Run(change, func(t *testing.T) {
			r, err := NewRegistry(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Update(func(cfg *ModelsFile) error {
				cfg.Providers["local"] = Config{Name: "Local", API: "completions", BaseURL: "https://models.example/v1", Models: []ModelSeed{{ID: "old"}}, SyncedModels: []string{"old"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			key := "secret"
			if err := r.SetCredential("local", &key); err != nil {
				t.Fatal(err)
			}
			p, _, _, err := r.ProviderCredential("local")
			if err != nil {
				t.Fatal(err)
			}
			models := []ModelSeed{{ID: "new"}}
			want := ErrProviderConfigChanged
			switch change {
			case "api", "base":
				err = r.Update(func(cfg *ModelsFile) error {
					pc := cfg.Providers[p.ID]
					if change == "api" {
						pc.API = "anthropic"
					} else {
						pc.BaseURL = "https://changed.example/v1"
					}
					cfg.Providers[p.ID] = pc
					return nil
				})
			case "key":
				changed := "changed"
				err = r.SetCredential(p.ID, &changed)
			case "extension":
				err = r.Update(func(cfg *ModelsFile) error { delete(cfg.Providers, p.ID); return nil })
				if err == nil {
					err = r.ReplaceExtensionProviders([]ExtensionProviderSpec{{ID: p.ID, Name: "Extension", API: p.API, BaseURL: p.BaseURL, Models: []ModelSeed{{ID: "extension"}}}})
				}
				if err == nil {
					err = r.SetCredential(p.ID, &key)
				}
			case "empty":
				models, want = nil, errAtLeastOneModel
			case "duplicate":
				models, want = []ModelSeed{{ID: "same"}, {ID: "same"}}, errDuplicateModel
			case "blank":
				models, want = []ModelSeed{{ID: " "}}, errModelIDRequired
			}
			if err != nil {
				t.Fatal(err)
			}
			before := r.UserConfig()
			if err := r.SyncModelsIfCurrent(p.ID, p.API, p.BaseURL, key, models); !errors.Is(err, want) {
				t.Fatalf("sync error = %v, want %v", err, want)
			}
			if !reflect.DeepEqual(before, r.UserConfig()) {
				t.Fatal("rejected sync mutated registry")
			}
		})
	}
}

func TestResolveSpecPrefersKnownProviderThenFallbackModelID(t *testing.T) {
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	on, off, reasoning := true, false, false
	if err := r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["magpie"] = Config{
			Name: "Magpie", API: "completions", BaseURL: "http://127.0.0.1:1/v1", Enabled: &on,
			Models: []ModelSeed{
				{ID: "codex/gpt-6.1-sol", ContextWindow: 8192, MaxTokens: 1024, Input: []string{"text"}, Reasoning: &reasoning},
				{ID: "vendor/other", ContextWindow: 8192, MaxTokens: 1024, Input: []string{"text"}, Reasoning: &reasoning},
				{ID: "solo", ContextWindow: 8192, MaxTokens: 1024, Input: []string{"text"}, Reasoning: &reasoning},
			},
		}
		cfg.Providers["codex"] = Config{
			Name: "Codex", API: "responses", BaseURL: "http://127.0.0.1:2/v1", Enabled: &on,
			Models: []ModelSeed{{ID: "gpt-6.1-sol", ContextWindow: 8192, MaxTokens: 1024, Input: []string{"text"}, Reasoning: &reasoning}},
		}
		cfg.Providers["disabled"] = Config{
			Name: "Disabled", API: "responses", BaseURL: "http://127.0.0.1:3/v1", Enabled: &off,
			Models: []ModelSeed{{ID: "vendor/model", ContextWindow: 8192, MaxTokens: 1024, Input: []string{"text"}, Reasoning: &reasoning}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, spec, fallback, provider, model string
	}{
		{"nested spec", "magpie/codex/gpt-6.1-sol", "", "magpie", "codex/gpt-6.1-sol"},
		{"unique owner", "vendor/other", "", "magpie", "vendor/other"},
		{"fallback nested id", "vendor/other", "magpie", "magpie", "vendor/other"},
		{"bare id", "solo", "magpie", "magpie", "solo"},
		{"provider wins", "codex/gpt-6.1-sol", "magpie", "codex", "gpt-6.1-sol"},
		{"disabled provider", "disabled/vendor/model", "", "disabled", "vendor/model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, model, err := r.ResolveSpec(tc.spec, tc.fallback)
			if tc.provider == "disabled" {
				if err == nil || !strings.Contains(err.Error(), `provider "disabled" is unavailable`) {
					t.Fatalf("disabled provider = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ref.Provider != tc.provider || ref.Model != tc.model || model.ID != tc.model {
				t.Fatalf("resolve %q/%q = %+v %q", tc.spec, tc.fallback, ref, model.ID)
			}
		})
	}
}
