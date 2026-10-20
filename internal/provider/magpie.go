package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type magpieModelList struct {
	Data json.RawMessage `json:"data"`
}

type magpieModel struct {
	ID                       string                 `json:"id"`
	DisplayName              string                 `json:"display_name"`
	Reasoning                *bool                  `json:"reasoning"`
	SupportedReasoningLevels []magpieReasoningLevel `json:"supported_reasoning_levels"`
	ReasoningLevels          []string               `json:"reasoning_levels"`
	ContextWindow            int                    `json:"context_window"`
	MaxOutputTokens          int                    `json:"max_output_tokens"`
	Modalities               json.RawMessage        `json:"modalities"`
}

type magpieReasoningLevel struct {
	Effort string `json:"effort"`
}

// ParseMagpieModels converts Magpie's OpenAI-compatible catalog into Ki model
// seeds. Unknown fields are intentionally ignored because Magpie adds metadata
// independently of the subset Ki currently understands.
func ParseMagpieModels(raw []byte) ([]ModelSeed, error) {
	var list magpieModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode Magpie model list: %w", err)
	}
	if len(list.Data) == 0 || bytes.Equal(bytes.TrimSpace(list.Data), []byte("null")) {
		return nil, fmt.Errorf("Magpie model list is missing data")
	}
	var remote []magpieModel
	if err := json.Unmarshal(list.Data, &remote); err != nil {
		return nil, fmt.Errorf("decode Magpie model data: %w", err)
	}

	out := make([]ModelSeed, 0, len(remote))
	seen := make(map[string]bool, len(remote))
	for _, source := range remote {
		if strings.TrimSpace(source.ID) == "" {
			return nil, fmt.Errorf("Magpie model list contains an empty model id")
		}
		if seen[source.ID] {
			return nil, fmt.Errorf("Magpie model list contains duplicate model id %q", source.ID)
		}
		seen[source.ID] = true

		name := source.DisplayName
		if strings.TrimSpace(name) == "" {
			name = source.ID
		}
		seed := ModelSeed{
			// Magpie IDs include namespaces and route-group names. Keep them
			// byte-for-byte: this is the identifier sent back to its gateway.
			ID: source.ID, Name: name, ContextWindow: source.ContextWindow,
			MaxTokens: source.MaxOutputTokens, Input: []string{"text"}, Cost: nil,
		}
		// Magpie omits limits when an upstream does not publish them. Ki's
		// generic seed fallback is intentionally optimistic, so use smaller
		// import-time estimates here and let users tune them in model settings.
		if seed.ContextWindow <= 0 {
			seed.ContextWindow = 32_000
		}
		if seed.MaxTokens <= 0 {
			seed.MaxTokens = 4_096
		}
		if magpieSupportsImage(source.Modalities) {
			seed.Input = []string{"text", "image"}
		}

		levels := make([]string, 0, len(source.SupportedReasoningLevels)+len(source.ReasoningLevels))
		for _, level := range source.SupportedReasoningLevels {
			levels = append(levels, level.Effort)
		}
		if len(levels) == 0 {
			levels = append(levels, source.ReasoningLevels...)
		}
		mappedLevels := canonicalMagpieEfforts(levels)
		reasoning := len(mappedLevels) > 0 && (source.Reasoning == nil || *source.Reasoning)
		seed.Reasoning = &reasoning
		if reasoning {
			// Ki treats unmapped basic levels as supported. Explicit nil entries
			// prevent sparse Magpie metadata from advertising levels it did not list.
			levelMap := make(map[string]*string, len(thinkingLevels))
			for _, level := range thinkingLevels {
				levelMap[level] = nil
			}
			for level, effort := range mappedLevels {
				value := effort
				levelMap[level] = &value
			}
			seed.ThinkingLevelMap = levelMap
			seed.Compat.SupportsReasoningEffort = true
		}
		out = append(out, seed)
	}
	return out, nil
}

func magpieSupportsImage(raw json.RawMessage) bool {
	var modalities []string
	if json.Unmarshal(raw, &modalities) == nil {
		return hasImageModality(modalities)
	}

	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	// Magpie's current model catalog uses the OpenRouter-style
	// {"input":["text","image"],"output":["text"]} shape. Older catalog
	// producers may use a flat modality map, so accept that too.
	for _, key := range []string{"input", "input_modalities"} {
		if input, ok := object[key]; ok {
			return modalityValueHasImage(input)
		}
	}
	if _, hasOutput := object["output"]; hasOutput {
		return false
	}
	return modalityObjectHasImage(object)
}

func modalityValueHasImage(raw json.RawMessage) bool {
	var modalities []string
	if json.Unmarshal(raw, &modalities) == nil {
		return hasImageModality(modalities)
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && modalityObjectHasImage(object)
}

func modalityObjectHasImage(object map[string]json.RawMessage) bool {
	for key, value := range object {
		if !strings.EqualFold(key, "image") {
			continue
		}
		var enabled bool
		if json.Unmarshal(value, &enabled) == nil {
			return enabled
		}
		var name string
		return json.Unmarshal(value, &name) == nil && name != ""
	}
	return false
}

func hasImageModality(modalities []string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			return true
		}
	}
	return false
}

func canonicalMagpieEfforts(levels []string) map[string]string {
	out := make(map[string]string, len(levels))
	for _, raw := range levels {
		effort := strings.ToLower(strings.TrimSpace(raw))
		switch effort {
		case "off":
			out["off"] = "off"
		case "none":
			out["off"] = "none"
		case "minimal", "low", "medium", "high", "xhigh", "max":
			out[effort] = effort
		}
	}
	return out
}
