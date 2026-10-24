package types

import (
	"encoding/json"
	"strings"
)

func TextTokens(text string) int { return (len(text) + 3) / 4 }

func MessageTokens(message Message) int {
	total := 0
	for _, block := range message.Content {
		switch block.Type {
		case "text", "":
			total += TextTokens(block.Text)
		case "toolCall":
			input := block.Input
			if block.ToolType != "custom" {
				input = block.ArgumentsRaw
				if input == "" {
					raw, _ := json.Marshal(block.Arguments)
					input = string(raw)
				}
			}
			total += TextTokens(block.Name + "\n" + input)
		case "image":
			total += 2048
		case "thinking":
			if block.ThinkingSignature != "" {
				total += opaqueTokens(block.ThinkingSignature)
			}
		}
	}
	return total
}

func opaqueTokens(encoded string) int {
	return (len(encoded)*3/4 + 3) / 4
}

func ResponseItemTokens(raw json.RawMessage) int {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil {
		return (len(raw) + 3) / 4
	}
	var kind string
	_ = json.Unmarshal(item["type"], &kind)
	var text string
	if kind == "compaction" || kind == "context_compaction" || kind == "reasoning" {
		_ = json.Unmarshal(item["encrypted_content"], &text)
		return opaqueTokens(text)
	}
	if kind == "input_image" || kind == "image_url" {
		return 2048
	}
	total := 0
	for _, key := range []string{"text", "name", "arguments", "input"} {
		if json.Unmarshal(item[key], &text) == nil {
			total += TextTokens(text)
		}
	}
	for _, key := range []string{"content", "output"} {
		if value, exists := item[key]; exists {
			var parts []json.RawMessage
			if json.Unmarshal(value, &parts) == nil {
				for _, part := range parts {
					total += ResponseItemTokens(part)
				}
			} else if json.Unmarshal(value, &text) == nil {
				total += TextTokens(text)
			} else if len(value) > 0 && strings.TrimSpace(string(value)) != "null" {
				total += (len(value) + 3) / 4
			}
		}
	}
	return total
}
