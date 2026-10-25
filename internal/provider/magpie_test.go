package provider

import (
	"reflect"
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

func modelMetadataPointer[T any](value T) *T {
	return &value
}

func TestParseProviderModelsPreservesKnownMetadata(t *testing.T) {
	model := Model{
		ID: "known", Name: "Configured name", API: "responses", BaseURL: "https://configured.example/v1", Enabled: false,
		ContextWindow: 200_000, MaxTokens: 30_000, Input: []string{"text", "image"},
		ApplyPatchToolType: "freeform", ExecToolType: "freeform", Compaction: CompactionCapabilities{Inline: "openai"},
		Reasoning: true, ThinkingLevelMap: map[string]*string{"off": nil, "high": modelMetadataPointer("high")},
		Cost:   &Cost{CostRates: CostRates{Input: 2, Output: 8, CacheRead: 0.2}, Tiers: []CostTier{{InputTokensAbove: 100_000, CostRates: CostRates{Input: 4, Output: 12}}}},
		Compat: Compat{ThinkingFormat: "openai", SupportsReasoningEffort: true, SupportsDeveloperRole: modelMetadataPointer(false), MaxTokensField: "max_completion_tokens", RequiresReasoningContent: true},
	}
	for _, extra := range []string{"", `,"display_name":null,"context_window":null,"max_output_tokens":null,"modalities":null,"reasoning":null`, `,"modalities":{"output":["text"]}`} {
		t.Run(extra, func(t *testing.T) {
			seeds, err := ParseProviderModels([]byte(`{"data":[{"id":"known"`+extra+`}]}`), []Model{model})
			if err != nil {
				t.Fatal(err)
			}
			got := resolveSeed("local", "completions", "https://provider.example", seeds[0], false)
			want := model
			want.Provider, want.Customized = "local", true
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("missing metadata changed known model:\ngot  %+v\nwant %+v", got, want)
			}
			*seeds[0].ThinkingLevelMap["high"] = "low"
			seeds[0].Input[0] = "changed"
			seeds[0].Cost.Tiers[0].Input = 99
			*seeds[0].Compat.SupportsDeveloperRole = true
			if *model.ThinkingLevelMap["high"] != "high" || model.Input[0] != "text" || model.Cost.Tiers[0].Input != 4 || *model.Compat.SupportsDeveloperRole {
				t.Fatal("parsed metadata aliases existing model")
			}
		})
	}
}

func TestParseProviderModelsMapsCatalogFormats(t *testing.T) {
	for _, tc := range []struct {
		name, raw, display string
		context, output    int
		image              bool
	}{
		{"openai sparse", `{"id":"gpt-image-reasoning"}`, "gpt-image-reasoning", 32_000, 4_096, false},
		{"anthropic", `{"id":"claude","display_name":"Claude","max_input_tokens":200000,"max_tokens":64000,"capabilities":{"image_input":{"supported":true},"thinking":{"supported":true,"types":["adaptive"]}}}`, "Claude", 200_000, 64_000, true},
		{"openrouter", `{"id":"vendor/model","name":"Router model","context_length":262144,"top_provider":{"max_completion_tokens":32768},"architecture":{"input_modalities":["text","image"]},"pricing":{"prompt":"0.000003","completion":"0.000015"}}`, "Router model", 262_144, 32_768, true},
		{"deepseek", `{"id":"deepseek-flash","name":"Flash","context_window":1000000,"max_output_tokens":384000,"input_modalities":["Text","Image"]}`, "Flash", 1_000_000, 384_000, true},
		{"priority", `{"id":"m","name":"Name","display_name":"Display","context_window":100000,"max_input_tokens":200000,"context_length":300000,"max_output_tokens":8000,"max_tokens":9000,"top_provider":{"max_completion_tokens":10000},"modalities":{"input":["text"],"output":["image"]}}`, "Display", 100_000, 8_000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seeds, err := ParseProviderModels([]byte(`{"data":[`+tc.raw+`]}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := seeds[0]
			if got.Name != tc.display || got.ContextWindow != tc.context || got.MaxTokens != tc.output || hasImageModality(got.Input) != tc.image || got.Cost != nil || got.Reasoning == nil || *got.Reasoning || got.ThinkingLevelMap != nil {
				t.Fatalf("model metadata = %+v", got)
			}
		})
	}
}

func TestParseProviderModelsUpdatesOnlyExplicitMetadata(t *testing.T) {
	known := Model{ID: "m", Name: "Existing", Enabled: false, ContextWindow: 100000, MaxTokens: 12000, Input: []string{"text", "image"}, Reasoning: true, ThinkingLevelMap: map[string]*string{"high": modelMetadataPointer("high")}, Cost: &Cost{CostRates: CostRates{Input: 2, Output: 8}}, Compat: Compat{ThinkingFormat: "deepseek", RequiresReasoningContent: true}}
	seeds, err := ParseProviderModels([]byte(`{"data":[{"id":"m","name":"Updated","max_input_tokens":200000,"capabilities":{"image_input":{"supported":false}},"pricing":{"prompt":"0","completion":"0"}}]}`), []Model{known})
	if err != nil {
		t.Fatal(err)
	}
	got := seeds[0]
	if got.Name != "Updated" || got.ContextWindow != 200000 || got.MaxTokens != 12000 || len(got.Input) != 1 || *got.Enabled || !*got.Reasoning || got.Cost.Input != 2 || !reflect.DeepEqual(got.Compat, known.Compat) || !reflect.DeepEqual(got.ThinkingLevelMap, known.ThinkingLevelMap) {
		t.Fatalf("explicit update = %+v", got)
	}
	seeds, err = ParseProviderModels([]byte(`{"data":[{"id":"m","effort":{"supported_levels":["none","low","high","future"],"default_level":"high"}}]}`), []Model{known})
	if err != nil {
		t.Fatal(err)
	}
	got = seeds[0]
	if !*got.Reasoning || !got.Compat.SupportsReasoningEffort || got.Compat.ThinkingFormat != "deepseek" || *got.ThinkingLevelMap["off"] != "none" || *got.ThinkingLevelMap["low"] != "low" || got.ThinkingLevelMap["medium"] != nil {
		t.Fatalf("explicit effort metadata = %+v", got)
	}
}

func TestParseProviderModelsRejectsInvalidCatalog(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `[]`, `{"data":null}`, `{"data":{}}`, `{"data":"models"}`, `{"data":[null]}`, `{"data":[{}]}`, `{"data":[{"id":" "}]}`, `{"data":[{"id":"same"},{"id":"same"}]}`, `{"data":[{"id":1}]}`, `not-json`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseProviderModels([]byte(raw), nil); err == nil {
				t.Fatal("expected invalid model catalog error")
			}
		})
	}
	models, err := ParseProviderModels([]byte(`{"data":[]}`), nil)
	if err != nil || models == nil || len(models) != 0 {
		t.Fatalf("empty catalog = %+v, %v", models, err)
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
