package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ki/internal/loop"
	"ki/internal/state"
	"ki/internal/types"
)

func TestFastThinkingLevelsPairEverySupportedBase(t *testing.T) {
	m := Model{
		Reasoning: true, FastServiceTier: "priority",
		ThinkingLevelMap: map[string]*string{
			"off": ptrLevel("none"), "minimal": ptrLevel("low"),
			"low": ptrLevel("low"), "medium": ptrLevel("medium"),
			"high": ptrLevel("high"), "xhigh": ptrLevel("xhigh"), "max": ptrLevel("max"),
		},
	}
	var want []string
	for _, base := range thinkingLevels {
		want = append(want, base, base+" fast")
	}
	if got := SupportedThinkingLevels(m); !slices.Equal(got, want) {
		t.Fatalf("levels = %v, want %v", got, want)
	}
	if got := DefaultThinking(m); got != "medium" {
		t.Fatalf("default = %q, want standard medium", got)
	}
	for _, hidden := range thinkingLevels {
		t.Run("hide_"+hidden, func(t *testing.T) {
			copy := m
			copy.ThinkingLevelMap = cloneThinkingMap(m.ThinkingLevelMap)
			copy.ThinkingLevelMap[hidden] = nil
			got := SupportedThinkingLevels(copy)
			if slices.Contains(got, hidden) || slices.Contains(got, hidden+" fast") {
				t.Fatalf("hidden level %q remains in %v", hidden, got)
			}
		})
	}
	m.FastServiceTier = ""
	if got := SupportedThinkingLevels(m); !slices.Equal(got, thinkingLevels) {
		t.Fatalf("ordinary levels = %v", got)
	}
	for _, model := range []Model{{}, {FastServiceTier: "priority"}} {
		want := []string{"off"}
		if model.FastServiceTier != "" {
			want = append(want, "off fast")
		}
		if got := SupportedThinkingLevels(model); !slices.Equal(got, want) {
			t.Fatalf("non-reasoning levels = %v, want %v", got, want)
		}
		if got := DefaultThinking(model); got != "off" {
			t.Fatalf("non-reasoning default = %q", got)
		}
	}
	for _, p := range BuiltinProviders() {
		for _, model := range p.Models {
			for _, level := range SupportedThinkingLevels(model) {
				if strings.HasSuffix(level, " fast") {
					t.Errorf("ordinary builtin %s/%s unexpectedly advertises %q", p.ID, model.ID, level)
				}
			}
		}
	}
}

func TestClampFastThinkingPreservesIntentAcrossModels(t *testing.T) {
	fast := Model{Reasoning: true, FastServiceTier: "priority"}
	sparse := fast
	sparse.ThinkingLevelMap = map[string]*string{
		"off": nil, "minimal": nil, "low": nil, "medium": nil, "xhigh": nil, "max": nil,
	}
	plain := sparse
	plain.FastServiceTier = ""
	allHidden := fast
	allHidden.ThinkingLevelMap = map[string]*string{}
	for _, level := range thinkingLevels {
		allHidden.ThinkingLevelMap[level] = nil
	}
	for _, tc := range []struct {
		name, requested, want string
		model                 Model
		err                   error
	}{
		{"exact", "high fast", "high fast", fast, nil},
		{"off", "off fast", "off fast", fast, nil},
		{"nearest_up_fast", "medium fast", "high fast", sparse, nil},
		{"nearest_down_fast", "max fast", "high fast", sparse, nil},
		{"drop_fast_up", "medium fast", "high", plain, nil},
		{"drop_fast_down", "max fast", "high", plain, nil},
		{"keep_plain", "medium", "high", sparse, nil},
		{"drop_fast_keep_base", "high fast", "high", Model{Reasoning: true}, nil},
		{"non_reasoning", "high fast", "off", Model{}, nil},
		{"non_reasoning_fast", "high fast", "off fast", Model{FastServiceTier: "priority"}, nil},
		{"empty", "", "", fast, nil},
		{"none_available", "high fast", "", allHidden, errNoThinkingEffort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ClampThinking(tc.model, tc.requested)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("ClampThinking(%q) = %q, %v; want %q, %v", tc.requested, got, err, tc.want, tc.err)
			}
		})
	}
	for _, invalid := range []string{"fast", "unknown fast", "HIGH fast", "high-fast", "high  fast", "high fast ", " high fast", "high fast fast"} {
		for _, model := range []Model{fast, plain} {
			if _, err := ClampThinking(model, invalid); !errors.Is(err, errInvalidThinkingEffort) {
				t.Errorf("ClampThinking(%q) error = %v", invalid, err)
			}
		}
	}
}

func TestFastServiceTierValidationPreservesThinkingMapWhitelist(t *testing.T) {
	model := resolveSeed("custom", "responses", "https://example.test/v1", ModelSeed{ID: "m"}, false)
	for _, tier := range []string{"", "priority", "fast", "flex", "default", " priority", "priority "} {
		model.FastServiceTier = tier
		err := validateExtensionModel(model)
		wantValid := tier == "" || tier == "priority"
		if (err == nil) != wantValid {
			t.Errorf("extension tier %q validation = %v; valid=%v", tier, err, wantValid)
		}
		err = validateModel(model)
		if (err == nil) != (tier == "") {
			t.Errorf("core tier %q validation = %v; only empty is valid", tier, err)
		}
		if tier != "" && (err == nil || !strings.Contains(err.Error(), "requires an extension provider")) {
			t.Errorf("core tier %q error must explain ownership: %v", tier, err)
		}
	}
	model.FastServiceTier = "priority"
	model.ThinkingLevelMap = map[string]*string{"high fast": ptrLevel("high")}
	if err := validateExtensionModel(model); !errors.Is(err, errInvalidThinkingLevel) {
		t.Fatalf("extension composite map key must remain invalid, got %v", err)
	}
	model.FastServiceTier = ""
	if err := validateModel(model); !errors.Is(err, errInvalidThinkingLevel) {
		t.Fatalf("core composite map key must remain invalid, got %v", err)
	}
}

func TestFastServiceTierCatalogMergeCloneAndPersistence(t *testing.T) {
	priority, empty, reasoning := "priority", "", true
	// Definition documents share the seed/override schema with the registry.
	// Loading an extension-owned capability into the core registry is tested
	// separately below; this checks its lossless storage and clone contract.
	cfg := ModelsFile{Providers: map[string]Config{
		"custom": {
			Name: "Custom", API: "openai-codex-responses", BaseURL: "https://example.test/v1",
			Models:         []ModelSeed{{ID: "m", Reasoning: &reasoning, FastServiceTier: priority}},
			ModelOverrides: map[string]ModelOverride{"m": {FastServiceTier: &priority}},
		},
	}}
	clone := cloneModelsFile(cfg)
	override := clone.Providers["custom"].ModelOverrides["m"]
	*override.FastServiceTier = "mutated"
	clone.Providers["custom"].Models[0].FastServiceTier = "mutated"
	if got := *cfg.Providers["custom"].ModelOverrides["m"].FastServiceTier; got != priority {
		t.Fatalf("override clone leaked pointer mutation: %q", got)
	}
	if got := cfg.Providers["custom"].Models[0].FastServiceTier; got != priority {
		t.Fatalf("seed clone leaked mutation: %q", got)
	}
	path := filepath.Join(t.TempDir(), "model-definitions.json")
	if err := state.WriteVersioned(path, 1, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _, err := state.ReadFile(path, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded ModelsFile
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.Providers["custom"]
	model := resolveSeed("custom", persisted.API, persisted.BaseURL, persisted.Models[0], false)
	if model.FastServiceTier != priority || *persisted.ModelOverrides["m"].FastServiceTier != priority {
		t.Fatalf("definition roundtrip lost capability: %s", raw)
	}
	model, err = applyModelOverride(model, ModelOverride{FastServiceTier: &empty})
	if err != nil || model.FastServiceTier != "" {
		t.Fatalf("override did not clear capability: %+v, %v", model, err)
	}
	model, err = applyModelOverride(model, ModelOverride{FastServiceTier: &priority})
	if err != nil || model.FastServiceTier != priority {
		t.Fatalf("override did not restore capability: %+v, %v", model, err)
	}
	spec := ExtensionProviderSpec{
		ID: "custom", Name: persisted.Name, API: persisted.API, BaseURL: persisted.BaseURL, Models: persisted.Models,
	}
	r, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReplaceExtensionProviders([]ExtensionProviderSpec{spec}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, view := range r.Providers() {
		if view.ID == "custom" {
			found = true
			if view.Models[0].FastServiceTier != priority {
				t.Fatalf("extension registry lost capability: %+v", view)
			}
			view.Models[0].FastServiceTier = "mutated"
		}
	}
	if !found {
		t.Fatal("extension registry provider missing")
	}
	for _, view := range r.Providers() {
		if view.ID == "custom" && view.Models[0].FastServiceTier != priority {
			t.Fatal("provider view mutation leaked into registry")
		}
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["openai"] = Config{ModelOverrides: map[string]ModelOverride{
			"gpt-6-astra": {FastServiceTier: &priority},
		}}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "requires an extension provider") {
		t.Fatalf("core override claiming Fast must fail clearly: %v", err)
	}
	if err := r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["core"] = Config{
			Name: "Core", API: "responses", BaseURL: "https://example.test/v1",
			Models: []ModelSeed{{ID: "m", Reasoning: &reasoning, FastServiceTier: priority}},
		}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "requires an extension provider") {
		t.Fatalf("core seed claiming Fast must fail clearly: %v", err)
	}
}

func TestLiveRejectsFastThinkingBeforeTransport(t *testing.T) {
	for _, api := range []string{"completions", "responses", "anthropic"} {
		for _, level := range thinkingLevels {
			t.Run(api+"/"+level, func(t *testing.T) {
				called, emitted := false, false
				live := NewLive(api, "https://example.test/v1", "key", roundTrip(func(*http.Request) (*http.Response, error) {
					called = true
					return nil, errors.New("unexpected request")
				}))
				req := loop.Request{ThinkingEffort: level + " fast", Model: "m"}
				_, streamErr := live.Stream(context.Background(), req, func(loop.AssistantDelta) error {
					emitted = true
					return nil
				})
				_, compactErr := live.Compact(context.Background(), req)
				for _, err := range []error{streamErr, compactErr} {
					var marker interface{ NonRetryable() bool }
					if err == nil || !strings.Contains(err.Error(), "requires an extension provider") ||
						!errors.As(err, &marker) || !marker.NonRetryable() {
						t.Errorf("Fast request must fail deterministically: %v", err)
					}
				}
				if called || emitted {
					t.Fatalf("rejection occurred too late: called=%v emitted=%v", called, emitted)
				}
			})
		}
	}
	// Ordinary selections still reach the transport rather than the Fast guard.
	called := false
	live := NewLive("responses", "https://example.test/v1", "key", roundTrip(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("test transport")
	}))
	_, _ = live.Stream(context.Background(), loop.Request{
		Model: "m", ThinkingEffort: "high",
		Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hello"}}}},
	}, nil)
	if !called {
		t.Fatal("ordinary selection did not reach transport")
	}
}

func TestBundledCodexModelsDeclareFastServiceTier(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "extensions", "codex-oauth", "extension.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Providers []ExtensionProviderSpec `json:"providers"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Providers) != 1 || len(manifest.Providers[0].Models) != 6 {
		t.Fatalf("unexpected bundled catalog: %+v", manifest)
	}
	p, err := BuildExtensionProvider(manifest.Providers[0])
	if err != nil {
		t.Fatal(err)
	}
	cloned := cloneProvider(p)
	for i, model := range p.Models {
		if model.FastServiceTier != "priority" || cloned.Models[i].FastServiceTier != "priority" {
			t.Errorf("%s lost Fast capability", model.ID)
		}
		plain := model
		plain.FastServiceTier = ""
		var want []string
		for _, base := range SupportedThinkingLevels(plain) {
			want = append(want, base, base+" fast")
		}
		if got := SupportedThinkingLevels(model); !slices.Equal(got, want) {
			t.Errorf("%s levels=%v, want %v", model.ID, got, want)
		}
		if DefaultThinking(model) != DefaultThinking(plain) {
			t.Errorf("%s Fast capability changed its default", model.ID)
		}
	}
}
