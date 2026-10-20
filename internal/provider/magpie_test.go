package provider

import (
	"strings"
	"testing"
)

func TestParseMagpieModelsMapsCatalogMetadata(t *testing.T) {
	raw := []byte(`{
		"object":"list",
		"data":[
			{
				"id":"vendor/model/nested",
				"display_name":"Display model",
				"owned_by":"vendor",
				"reasoning":true,
				"supported_reasoning_levels":[{"effort":"none"},{"effort":"low"},{"effort":"high"}],
				"reasoning_levels":["max"],
				"context_window":262144,
				"max_output_tokens":32768,
				"modalities":{"input":["text","image"],"output":["text"]}
			},
			{"id":"group/daily","display_name":"Daily route","modalities":{"input":["text"],"output":["text"]}}
		]
	}`)
	models, err := ParseMagpieModels(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	got := models[0]
	if got.ID != "vendor/model/nested" || got.Name != "Display model" ||
		got.ContextWindow != 262144 || got.MaxTokens != 32768 ||
		len(got.Input) != 2 || got.Input[1] != "image" || got.Cost != nil {
		t.Fatalf("model metadata = %+v", got)
	}
	if got.Reasoning == nil || !*got.Reasoning || !got.Compat.SupportsReasoningEffort {
		t.Fatalf("reasoning metadata = %+v", got)
	}
	if got.ThinkingLevelMap["off"] == nil || *got.ThinkingLevelMap["off"] != "none" ||
		got.ThinkingLevelMap["low"] == nil || got.ThinkingLevelMap["high"] == nil ||
		got.ThinkingLevelMap["medium"] != nil || got.ThinkingLevelMap["max"] != nil {
		t.Fatalf("sparse effort map = %+v", got.ThinkingLevelMap)
	}
	if models[1].ID != "group/daily" || models[1].Name != "Daily route" ||
		models[1].Reasoning == nil || *models[1].Reasoning ||
		models[1].ThinkingLevelMap != nil || len(models[1].Input) != 1 {
		t.Fatalf("non-reasoning route = %+v", models[1])
	}
}

func TestParseMagpieModelsUsesPlainEffortFallbackAndUnknownLimits(t *testing.T) {
	models, err := ParseMagpieModels([]byte(`{"data":[{"id":"m","reasoning":true,"reasoning_levels":["minimal","medium","future"],"modalities":["text","image"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ContextWindow != 32_000 || models[0].MaxTokens != 4_096 ||
		models[0].ThinkingLevelMap["minimal"] == nil || models[0].ThinkingLevelMap["medium"] == nil ||
		models[0].ThinkingLevelMap["high"] != nil || len(models[0].Input) != 2 {
		t.Fatalf("model = %+v", models)
	}
	resolved := resolveSeed("magpie", "completions", "http://127.0.0.1:3425/v1", models[0], true)
	if resolved.ContextWindow != 32_000 || resolved.MaxTokens != 4_096 {
		t.Fatalf("imported conservative limits = %d/%d", resolved.ContextWindow, resolved.MaxTokens)
	}

	models, err = ParseMagpieModels([]byte(`{"data":[{"id":"no-reasoning-info","reasoning":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if models[0].Reasoning == nil || *models[0].Reasoning || models[0].ThinkingLevelMap != nil {
		t.Fatalf("missing effort list was over-advertised: %+v", models[0])
	}
}

func TestParseMagpieModelsRejectsInvalidCatalog(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"data":null}`,
		`{"data":[{"id":""}]}`,
		`{"data":[{"id":"same"},{"id":"same"}]}`,
		`{"data":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseMagpieModels([]byte(raw))
			if err == nil {
				t.Fatal("expected invalid model catalog error")
			}
		})
	}
	if _, err := ParseMagpieModels([]byte(`not-json`)); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("invalid JSON error = %v", err)
	}
}
