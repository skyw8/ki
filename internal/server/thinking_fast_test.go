package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"ki/internal/provider"
	"ki/internal/session"
)

func TestFastThinkingModelCatalogProjection(t *testing.T) {
	registry, err := provider.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reasoning, xhigh, max := true, "xhigh", "max"
	if err := registry.ReplaceExtensionProviders([]provider.ExtensionProviderSpec{{
		ID: "codex", Name: "Codex", API: "codex-responses", BaseURL: "https://example.invalid",
		Models: []provider.ModelSeed{{
			ID: "model", ContextWindow: 4096, MaxTokens: 512, Input: []string{"text"},
			Reasoning: &reasoning, FastServiceTier: "priority",
			ThinkingLevelMap: map[string]*string{"xhigh": &xhigh, "max": &max},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{registry: registry}
	response := httptest.NewRecorder()
	srv.models(response, httptest.NewRequest("GET", "/v1/models", nil))
	var models []struct {
		Spec            string   `json:"spec"`
		FastServiceTier string   `json:"fastServiceTier"`
		ThinkingLevels  []string `json:"thinkingLevels"`
		DefaultThinking string   `json:"defaultThinking"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if model.Spec != "codex/model" {
			continue
		}
		want := []string{"off", "off fast", "minimal", "minimal fast", "low", "low fast", "medium", "medium fast", "high", "high fast", "xhigh", "xhigh fast", "max", "max fast"}
		if model.FastServiceTier != "priority" || model.DefaultThinking != "medium" || !reflect.DeepEqual(model.ThinkingLevels, want) {
			t.Fatalf("catalog lost capability or level pairs: %+v", model)
		}
		return
	}
	t.Fatal("extension model absent from flat catalog")
}

func TestFastThinkingSessionSelectionSurvivesForkAndResume(t *testing.T) {
	model := provider.Model{
		Provider: "codex", ID: "model", Reasoning: true, FastServiceTier: "priority",
	}
	root := filepath.Join(t.TempDir(), "sessions")
	for _, level := range provider.SupportedThinkingLevels(model) {
		t.Run(level, func(t *testing.T) {
			resolved, err := resolveThinking(model, level)
			if err != nil || resolved != level {
				t.Fatalf("resolve %q = %q, %v", level, resolved, err)
			}
			original, err := session.CreateWithOptions(root, t.TempDir(), model.Provider, model.ID, session.CreateOptions{ThinkingEffort: resolved})
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			loaded, err := session.ReadConfig(original.Dir)
			if err != nil || loaded.ThinkingEffort != level {
				t.Fatalf("resume config=%+v err=%v", loaded, err)
			}
			fork, err := session.Fork(root, original)
			if err != nil {
				t.Fatal(err)
			}
			defer fork.Close()
			if fork.Config.ThinkingEffort != level {
				t.Fatalf("fork lost thinking: %+v", fork.Config)
			}
		})
	}
}

func TestLocalSummaryRetainsAllThinkingSelections(t *testing.T) {
	mapped := "high"
	model := provider.Model{
		Provider: "codex", ID: "model", API: "openai-codex-responses",
		Reasoning: true, FastServiceTier: "priority",
		ThinkingLevelMap: map[string]*string{"high": &mapped, "xhigh": &mapped, "max": &mapped},
	}
	for _, level := range provider.SupportedThinkingLevels(model) {
		t.Run(level, func(t *testing.T) {
			recorder := &reqRecorder{}
			srv := &Server{streamer: recorder}
			if _, _, err := srv.summarizer(context.Background(), "session", model, level).Summarize(context.Background(), "summary system", "summary input"); err != nil {
				t.Fatal(err)
			}
			if len(recorder.reqs) != 1 {
				t.Fatalf("summary requests=%d", len(recorder.reqs))
			}
			req := recorder.reqs[0]
			if req.ThinkingEffort != level || req.ThinkingLevelMap["high"] == nil || *req.ThinkingLevelMap["high"] != mapped || req.API != model.API {
				t.Fatalf("local summary lost model thinking: %+v", req)
			}
		})
	}
}
