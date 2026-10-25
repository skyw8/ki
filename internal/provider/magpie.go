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
			ID: source.ID, Name: name, ContextWindow: source.ContextWindow,
			MaxTokens: source.MaxOutputTokens, Input: []string{"text"}, Cost: nil,
		}
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

type providerModel struct {
	magpieModel
	Name            string          `json:"name"`
	MaxInputTokens  int             `json:"max_input_tokens"`
	ContextLength   int             `json:"context_length"`
	MaxTokens       int             `json:"max_tokens"`
	InputModalities json.RawMessage `json:"input_modalities"`
	Effort          *struct {
		SupportedLevels []string `json:"supported_levels"`
	} `json:"effort"`
	TopProvider *struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Architecture *struct {
		InputModalities json.RawMessage `json:"input_modalities"`
	} `json:"architecture"`
	Capabilities *struct {
		ImageInput *struct {
			Supported *bool `json:"supported"`
		} `json:"image_input"`
	} `json:"capabilities"`
}

func ParseProviderModels(raw []byte, existing []Model) ([]ModelSeed, error) {
	var list magpieModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode provider model list: %w", err)
	}
	data := bytes.TrimSpace(list.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, fmt.Errorf("provider model list is missing data")
	}
	var remote []providerModel
	if err := json.Unmarshal(data, &remote); err != nil {
		return nil, fmt.Errorf("decode provider model data: %w", err)
	}
	known := make(map[string]Model, len(existing))
	for _, model := range existing {
		known[model.ID] = model
	}
	seen := make(map[string]bool, len(remote))
	out := make([]ModelSeed, 0, len(remote))
	for _, source := range remote {
		if strings.TrimSpace(source.ID) == "" {
			return nil, fmt.Errorf("provider model list contains an empty model id")
		}
		if seen[source.ID] {
			return nil, fmt.Errorf("provider model list contains duplicate model id %q", source.ID)
		}
		seen[source.ID] = true
		reasoning := false
		seed := ModelSeed{ID: source.ID, Name: source.ID, ContextWindow: 32_000, MaxTokens: 4_096, Input: []string{"text"}, Reasoning: &reasoning}
		if model, ok := known[source.ID]; ok {
			enabled := model.Enabled
			reasoning = model.Reasoning
			seed = ModelSeed{
				ID: model.ID, Name: model.Name, Enabled: &enabled, API: model.API, BaseURL: model.BaseURL,
				ContextWindow: model.ContextWindow, MaxTokens: model.MaxTokens, Input: append([]string(nil), model.Input...),
				ApplyPatchToolType: model.ApplyPatchToolType, ExecToolType: model.ExecToolType, Compaction: model.Compaction,
				Reasoning: &reasoning, ThinkingLevelMap: cloneThinkingMap(model.ThinkingLevelMap), FastServiceTier: model.FastServiceTier,
				Cost: cloneCost(model.Cost), Compat: model.Compat,
			}
			if model.Compat.SupportsDeveloperRole != nil {
				supported := *model.Compat.SupportsDeveloperRole
				seed.Compat.SupportsDeveloperRole = &supported
			}
		}
		for _, name := range []string{source.DisplayName, source.Name} {
			if strings.TrimSpace(name) != "" {
				seed.Name = name
				break
			}
		}
		for _, limit := range []int{source.ContextWindow, source.MaxInputTokens, source.ContextLength} {
			if limit > 0 {
				seed.ContextWindow = limit
				break
			}
		}
		limits := []int{source.MaxOutputTokens, source.MaxTokens}
		if source.TopProvider != nil {
			limits = append(limits, source.TopProvider.MaxCompletionTokens)
		}
		for _, limit := range limits {
			if limit > 0 {
				seed.MaxTokens = limit
				break
			}
		}
		modalities := source.Modalities
		if hasProviderMetadata(source.InputModalities) {
			modalities = source.InputModalities
		}
		if source.Architecture != nil && hasProviderMetadata(source.Architecture.InputModalities) {
			modalities = source.Architecture.InputModalities
		}
		if image, known := providerImageInput(modalities); known {
			seed.Input = []string{"text"}
			if image {
				seed.Input = append(seed.Input, "image")
			}
		}
		if source.Capabilities != nil && source.Capabilities.ImageInput != nil && source.Capabilities.ImageInput.Supported != nil {
			seed.Input = []string{"text"}
			if *source.Capabilities.ImageInput.Supported {
				seed.Input = append(seed.Input, "image")
			}
		}
		levels := make([]string, 0, len(source.SupportedReasoningLevels)+len(source.ReasoningLevels))
		for _, level := range source.SupportedReasoningLevels {
			levels = append(levels, level.Effort)
		}
		if len(levels) == 0 {
			levels = append(levels, source.ReasoningLevels...)
		}
		if len(levels) == 0 && source.Effort != nil {
			levels = append(levels, source.Effort.SupportedLevels...)
		}
		if mapped := canonicalMagpieEfforts(levels); len(mapped) > 0 {
			seed.ThinkingLevelMap = make(map[string]*string, len(thinkingLevels))
			for _, level := range thinkingLevels {
				seed.ThinkingLevelMap[level] = nil
			}
			for level, effort := range mapped {
				value := effort
				seed.ThinkingLevelMap[level] = &value
			}
			reasoning = true
			seed.Compat.SupportsReasoningEffort = true
		}
		if source.Reasoning != nil {
			reasoning = *source.Reasoning
			if !reasoning {
				seed.ThinkingLevelMap = nil
				seed.Compat.SupportsReasoningEffort = false
			}
		}
		out = append(out, seed)
	}
	return out, nil
}

func hasProviderMetadata(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func providerImageInput(raw json.RawMessage) (bool, bool) {
	if !hasProviderMetadata(raw) {
		return false, false
	}
	var modalities []string
	if json.Unmarshal(raw, &modalities) == nil {
		return hasImageModality(modalities), true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false, false
	}
	for _, key := range []string{"input", "input_modalities"} {
		if input, ok := object[key]; ok {
			return providerImageInput(input)
		}
	}
	for key := range object {
		if strings.EqualFold(key, "text") || strings.EqualFold(key, "image") {
			return modalityObjectHasImage(object), true
		}
	}
	return false, false
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
