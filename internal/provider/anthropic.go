package provider

import (
	"context"
	"encoding/json"
	"net/http"

	"ki/internal/loop"
	"ki/internal/types"
)

// AnthropicBody is the Anthropic Messages payload.
func AnthropicBody(req loop.Request) map[string]any {
	var msgs []map[string]any
	// Follow pi: combine consecutive toolResults into one user message containing
	// multiple tool_result blocks. Anthropic permits images in each
	// tool_result.content, so do not split them into trailing user messages.
	history := replayable(req.Messages)
	for i := 0; i < len(history); i++ {
		m := history[i]
		if m.Role != "toolResult" {
			msgs = append(msgs, toAnthropicMessage(m))
			continue
		}
		var blocks []map[string]any
		for ; i < len(history) && history[i].Role == "toolResult"; i++ {
			blocks = append(blocks, anthropicToolResultBlock(history[i]))
		}
		i--
		msgs = append(msgs, map[string]any{"role": "user", "content": blocks})
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"messages":   msgs,
		"stream":     true,
	}
	if req.ThinkingEffort != "" {
		switch {
		case req.ThinkingEffort == "off":
			body["thinking"] = map[string]any{"type": "disabled"}
		case req.ForceAdaptiveThinking:
			body["thinking"] = map[string]any{"type": "adaptive"}
			body["output_config"] = map[string]any{"effort": mappedThinking(req)}
		default:
			budgets := map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384, "xhigh": 32768, "max": 65536}
			budget := budgets[req.ThinkingEffort]
			if budget == 0 {
				budget = 8192
			}
			if budget >= maxTokens {
				budget = maxTokens - 1024
			}
			if budget > 0 {
				body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
			}
		}
	}
	if req.System != "" {
		body["system"] = []map[string]any{{
			"type":          "text",
			"text":          req.System,
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			if t.Type == "custom" {
				continue
			}
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			})
		}
		body["tools"] = tools
	}
	return body
}

func anthropicToolResultBlock(m types.Message) map[string]any {
	content := any(m.Text())
	if media := anthropicMediaBlocks(m); len(media) > 0 {
		var blocks []map[string]any
		if text := m.Text(); text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
		} else {
			blocks = append(blocks, map[string]any{"type": "text", "text": "(see attached image)"})
		}
		blocks = append(blocks, media...)
		content = blocks
	}
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": m.ToolCallID,
		"content":     content,
		"is_error":    m.IsError,
	}
}

func toAnthropicMessage(m types.Message) map[string]any {
	if m.Role == "toolResult" {
		return map[string]any{
			"role":    "user",
			"content": []map[string]any{anthropicToolResultBlock(m)},
		}
	}
	if m.Role == "assistant" {
		var blocks []map[string]any
		for _, c := range m.Content {
			switch c.Type {
			case "thinking":
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": c.Thinking})
			case "toolCall":
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    c.ID,
					"name":  c.Name,
					"input": c.Arguments,
				})
			default:
				if c.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": c.Text})
				}
			}
		}
		return map[string]any{"role": "assistant", "content": blocks}
	}
	if media := anthropicMediaBlocks(m); len(media) > 0 {
		var blocks []map[string]any
		for _, c := range m.Content {
			if (c.Type == "text" || c.Type == "") && c.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": c.Text})
			}
		}
		blocks = append(blocks, media...)
		return map[string]any{"role": "user", "content": blocks}
	}
	return map[string]any{"role": "user", "content": m.Text()}
}

func anthropicMediaBlocks(m types.Message) []map[string]any {
	var out []map[string]any
	for _, c := range m.Content {
		if c.Type != "image" || c.Data == "" {
			continue
		}
		mime := c.MIMEType
		if mime == "" {
			mime = "image/png"
		}
		out = append(out, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mime,
				"data":       c.Data,
			},
		})
	}
	return out
}

func (l *Live) streamAnthropic(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	url := l.Base + "/v1/messages"
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Api-Key", l.APIKey)
	h.Set("Anthropic-Version", "2023-06-01")
	h.Set("Anthropic-Beta", "prompt-caching-2024-07-31")
	return l.postStream(ctx, url, AnthropicBody(req), h, emit, parseAnthropicSSE)
}

func parseAnthropicSSE(event, data string, acc *types.Message) (loop.AssistantDelta, bool) {
	var obj map[string]any
	if json.Unmarshal([]byte(data), &obj) != nil {
		return loop.AssistantDelta{}, false
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		typ = event
	}
	switch typ {
	case "content_block_delta":
		if delta, ok := obj["delta"].(map[string]any); ok {
			if t, _ := delta["type"].(string); t == "text_delta" {
				text, _ := delta["text"].(string)
				appendText(acc, text)
				return loop.AssistantDelta{Type: "text_delta", Delta: text, Partial: *acc}, true
			}
			if t, _ := delta["type"].(string); t == "thinking_delta" {
				th, _ := delta["thinking"].(string)
				appendThinking(acc, th)
				return loop.AssistantDelta{Type: "thinking_delta", Delta: th, Partial: *acc}, true
			}
			if t, _ := delta["type"].(string); t == "input_json_delta" {
				partial, _ := delta["partial_json"].(string)
				appendToolCallDelta(acc, "", "", "", partial, -1)
			}
		}
	case "content_block_start":
		if blk, ok := obj["content_block"].(map[string]any); ok {
			if t, _ := blk["type"].(string); t == "tool_use" {
				id, _ := blk["id"].(string)
				name, _ := blk["name"].(string)
				c := findOrAddToolCall(acc, id, "", name, -1)
				if input, ok := blk["input"]; ok && input != nil {
					switch v := input.(type) {
					case map[string]any:
						if len(v) > 0 {
							c.Arguments = v
							c.ArgumentsRaw = marshalArguments(v)
						}
					case string:
						appendToolArgs(c, v)
					}
				}
			}
		}
	case "message_delta":
		if u, ok := obj["usage"].(map[string]any); ok {
			if acc.Usage == nil {
				acc.Usage = &types.Usage{}
			}
			acc.Usage.Output = asInt(u["output_tokens"])
			acc.Usage.CacheRead = asInt(u["cache_read_input_tokens"])
			acc.Usage.CacheWrite = asInt(u["cache_creation_input_tokens"])
		}
		if delta, ok := obj["delta"].(map[string]any); ok {
			if sr, _ := delta["stop_reason"].(string); sr == "tool_use" {
				acc.StopReason = "toolUse"
			}
		}
	case "message_start":
		if msg, ok := obj["message"].(map[string]any); ok {
			if u, ok := msg["usage"].(map[string]any); ok {
				acc.Usage = &types.Usage{
					Input:      asInt(u["input_tokens"]),
					CacheRead:  asInt(u["cache_read_input_tokens"]),
					CacheWrite: asInt(u["cache_creation_input_tokens"]),
				}
			}
		}
	}
	return loop.AssistantDelta{}, false
}
