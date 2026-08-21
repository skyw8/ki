package provider

import (
	"context"
	"encoding/json"
	"strings"

	"ki/internal/loop"
	"ki/internal/types"
)

// CompletionsBody is the OpenAI chat completions payload (exported for tests).
func CompletionsBody(req loop.Request) map[string]any {
	msgs := []map[string]any{}
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	// Completions 的 role:tool 只能是字符串，图必须另走 user 的 image_url。
	// 但不能「每条 toolResult 立刻跟一条带图 user」：同一次 assistant.tool_calls
	// 对应的 role:tool 必须连在一起，中间插 user 多数网关会 400。
	//
	// 对齐 pi packages/ai/src/api/openai-completions.ts：
	//   1. 连续 toolResult（同一次并行工具）先全部编成 role:tool（纯文本）。
	//   2. 这一组里的图攒成一条 user，跟在这组 tool 后面。
	//   3. 更早轮次的图仍待在当时那一组后面，不挪到整段对话末尾
	//      （前缀字节稳定，才能命中 prompt cache）。
	// 无图时不插 user，行为和改之前的纯文本路径相同。
	history := replayable(req.Messages)
	for i := 0; i < len(history); i++ {
		m := history[i]
		if m.Role != "toolResult" {
			msgs = append(msgs, toOpenAIMessage(m))
			continue
		}
		var imgs []map[string]any
		for ; i < len(history) && history[i].Role == "toolResult"; i++ {
			tm := history[i]
			msgs = append(msgs, toOpenAIMessage(tm))
			imgs = append(imgs, openAIImageURLs(tm)...)
		}
		i--
		if extra := openAIToolImageFollowup(imgs); extra != nil {
			msgs = append(msgs, extra)
		}
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
	}
	if req.MaxTokens > 0 {
		field := req.MaxTokensField
		if field == "" {
			field = "max_tokens"
		}
		body[field] = req.MaxTokens
	}
	effort := mappedThinking(req)
	if req.ThinkingEffort != "" {
		on := req.ThinkingEffort != "off"
		switch req.ThinkingFormat {
		case "qwen":
			body["enable_thinking"] = on
			if on && req.SupportsReasoningEffort {
				body["reasoning_effort"] = effort
			}
		case "deepseek", "zai":
			body["thinking"] = map[string]any{"type": map[bool]string{true: "enabled", false: "disabled"}[on]}
			if on && req.SupportsReasoningEffort {
				body["reasoning_effort"] = effort
			}
		default:
			if req.SupportsReasoningEffort {
				body["reasoning_effort"] = effort
			}
		}
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			if t.Type == "custom" {
				continue
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = tools
	}
	return body
}

func toOpenAIMessage(m types.Message) map[string]any {
	switch m.Role {
	case "toolResult":
		text := m.Text()
		if text == "" && hasImage(m) {
			text = "(see attached image)"
		}
		return map[string]any{
			"role":         "tool",
			"tool_call_id": m.ToolCallID,
			"content":      text,
		}
	case "assistant":
		out := map[string]any{"role": "assistant"}
		var text strings.Builder
		var calls []map[string]any
		for _, c := range m.Content {
			switch c.Type {
			case "text", "":
				text.WriteString(c.Text)
			case "thinking":
				// OpenAI-compat reasoning_content
				out["reasoning_content"] = c.Thinking
			case "toolCall":
				calls = append(calls, map[string]any{
					"id":   c.ID,
					"type": "function",
					"function": map[string]any{
						"name":      c.Name,
						"arguments": marshalArguments(c.Arguments),
					},
				})
			}
		}
		if text.String() != "" {
			out["content"] = text.String()
		}
		if len(calls) > 0 {
			out["tool_calls"] = calls
		}
		return out
	default:
		if parts := openAIContentParts(m); parts != nil {
			return map[string]any{"role": "user", "content": parts}
		}
		return map[string]any{"role": "user", "content": m.Text()}
	}
}

func openAIContentParts(m types.Message) []map[string]any {
	var parts []map[string]any
	hasMedia := false
	for _, c := range m.Content {
		switch c.Type {
		case "image":
			if c.Data == "" {
				continue
			}
			hasMedia = true
			mime := c.MIMEType
			if mime == "" {
				mime = "image/png"
			}
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + mime + ";base64," + c.Data,
				},
			})
		case "text", "":
			if c.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": c.Text})
			}
		}
	}
	if !hasMedia {
		return nil
	}
	return parts
}

func hasImage(m types.Message) bool {
	for _, c := range m.Content {
		if c.Type == "image" && c.Data != "" {
			return true
		}
	}
	return false
}

func openAIImageURLs(m types.Message) []map[string]any {
	var imgs []map[string]any
	for _, p := range openAIContentParts(m) {
		if p["type"] == "image_url" {
			imgs = append(imgs, p)
		}
	}
	return imgs
}

func openAIToolImageFollowup(imgs []map[string]any) map[string]any {
	if len(imgs) == 0 {
		return nil
	}
	content := append([]map[string]any{{
		"type": "text",
		"text": "Attached image(s) from tool result:",
	}}, imgs...)
	return map[string]any{"role": "user", "content": content}
}

func (l *Live) streamCompletions(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	url := l.Base + "/chat/completions"
	return l.postStream(ctx, url, CompletionsBody(req), l.oaHeaders(), emit, parseCompletionsSSE)
}

func parseCompletionsSSE(_, data string, acc *types.Message) (loop.AssistantDelta, bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			PromptCacheHit   int `json:"prompt_cache_hit_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return loop.AssistantDelta{}, false
	}
	if chunk.Usage != nil {
		cached := chunk.Usage.PromptCacheHit
		if cached == 0 {
			cached = chunk.Usage.PromptDetails.CachedTokens
		}
		acc.Usage = &types.Usage{
			Input:     chunk.Usage.PromptTokens,
			Output:    chunk.Usage.CompletionTokens,
			CacheRead: cached,
		}
	}
	if len(chunk.Choices) == 0 {
		return loop.AssistantDelta{}, false
	}
	d := chunk.Choices[0].Delta
	if d.Content != "" {
		appendText(acc, d.Content)
		return loop.AssistantDelta{Type: "text_delta", Delta: d.Content, Partial: *acc}, true
	}
	if d.ReasoningContent != "" {
		appendThinking(acc, d.ReasoningContent)
		return loop.AssistantDelta{Type: "thinking_delta", Delta: d.ReasoningContent, Partial: *acc}, true
	}
	for _, tc := range d.ToolCalls {
		idx := -1
		if tc.Index != nil {
			idx = *tc.Index
		}
		appendToolCallDelta(acc, tc.ID, "", tc.Function.Name, tc.Function.Arguments, idx)
	}
	switch chunk.Choices[0].FinishReason {
	case "tool_calls":
		acc.StopReason = "toolUse"
	case "length":
		acc.StopReason = "length"
	case "stop":
		acc.StopReason = "stop"
	}
	return loop.AssistantDelta{Type: "text_delta", Partial: *acc}, d.Content != "" || d.ReasoningContent != ""
}
