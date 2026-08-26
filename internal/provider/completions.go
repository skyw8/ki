package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Completions role:tool accepts only a string, so images must use a user image_url.
	// We cannot append an image-bearing user message after every toolResult: the
	// role:tool messages for one assistant.tool_calls batch must remain contiguous;
	// inserting a user message between them causes most gateways to return 400.
	//
	// This follows pi packages/ai/src/api/openai-completions.ts:
	//   1. Encode consecutive toolResults from one parallel batch as role:tool
	//      messages first, using plain text only.
	//   2. Collect that batch's images in one user message after the tool group.
	//   3. Keep images from earlier turns after their original group rather than
	//      moving them to the end of the conversation (stable prefix bytes preserve
	//      prompt-cache hits).
	// When there are no images, do not insert a user message; this matches the
	// previous plain-text path.
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
			if req.Provider == "openai" {
				field = "max_completion_tokens"
			}
		}
		body[field] = req.MaxTokens
	}
	if req.Provider == "openai" {
		body["stream_options"] = map[string]any{"include_usage": true}
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
		if len(tools) > 0 {
			body["tools"] = tools
		}
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
				if c.Thinking != "" {
					out["reasoning_content"] = c.Thinking
				}
			case "toolCall":
				arguments := validObjectArgumentsRaw(c.ArgumentsRaw)
				if arguments == "" {
					input := c.Arguments
					if input == nil {
						input = map[string]any{}
					}
					arguments = marshalArguments(input)
				}
				calls = append(calls, map[string]any{
					"id":   c.ID,
					"type": "function",
					"function": map[string]any{
						"name":      c.Name,
						"arguments": arguments,
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
	if err := validateCompletionsRequest(req); err != nil {
		return types.Message{Role: "assistant", StopReason: "error", ErrorMessage: err.Error()}, err
	}
	url := l.Base + "/chat/completions"
	msg, err := l.postStream(ctx, url, CompletionsBody(req), l.oaHeaders(), emit, parseCompletionsSSE, false)
	if err == nil && msg.StopReason == "toolUse" {
		if outputErr := validateCompletionsOutput(&msg); outputErr != nil {
			msg.StopReason = "error"
			msg.ErrorMessage = outputErr.Error()
			return msg, outputErr
		}
	}
	return msg, err
}

func parseCompletionsSSE(_, data string, acc *types.Message) sseParseResult {
	var chunk struct {
		ID      string `json:"id"`
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				Refusal          string `json:"refusal"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
				FunctionCall *struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function_call"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptCacheHit   int `json:"prompt_cache_hit_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return sseParseResult{err: errors.New("invalid chat completions SSE JSON")}
	}
	if chunk.ID != "" {
		acc.ResponseID = chunk.ID
	}
	if chunk.Usage != nil {
		cached := chunk.Usage.PromptCacheHit
		if cached == 0 {
			cached = chunk.Usage.PromptDetails.CachedTokens
		}
		acc.Usage = &types.Usage{
			// Chat Completions prompt_tokens includes cached tokens. Keep the
			// provider's counters verbatim here; CalculateCost normalizes them
			// once the resolved model is available.
			Input:       chunk.Usage.PromptTokens,
			Output:      chunk.Usage.CompletionTokens,
			CacheRead:   cached,
			TotalTokens: chunk.Usage.TotalTokens,
		}
		if acc.Usage.TotalTokens == 0 {
			acc.Usage.TotalTokens = chunk.Usage.PromptTokens + chunk.Usage.CompletionTokens
		}
	}
	if len(chunk.Choices) == 0 {
		return sseParseResult{}
	}
	d := chunk.Choices[0].Delta
	var deltas []loop.AssistantDelta
	if d.Content != "" {
		appendText(acc, d.Content)
		deltas = append(deltas, loop.AssistantDelta{Type: "text_delta", Delta: d.Content, Partial: *acc})
	}
	if d.Refusal != "" {
		appendText(acc, d.Refusal)
		deltas = append(deltas, loop.AssistantDelta{Type: "text_delta", Delta: d.Refusal, Partial: *acc})
	}
	if d.ReasoningContent != "" {
		appendThinking(acc, d.ReasoningContent)
		deltas = append(deltas, loop.AssistantDelta{Type: "thinking_delta", Delta: d.ReasoningContent, Partial: *acc})
	}
	for _, tc := range d.ToolCalls {
		if tc.Index == nil || *tc.Index < 0 {
			return sseParseResult{err: errors.New("Chat Completions tool call has no valid index")}
		}
		idx := *tc.Index
		id := tc.ID
		if id == "" {
			if call := completionToolCallAt(acc, idx); call != nil {
				id = call.ID
			}
			if id == "" {
				return sseParseResult{err: fmt.Errorf("Chat Completions tool call %d has no id", idx)}
			}
		}
		if tc.Function.Name == "" {
			if call := completionToolCallAt(acc, idx); call != nil {
				tc.Function.Name = call.Name
			}
		}
		appendToolCallDelta(acc, id, "", tc.Function.Name, tc.Function.Arguments, idx)
		call := completionToolCallAt(acc, idx)
		if call != nil && tc.Function.Arguments != "" {
			deltas = append(deltas, loop.AssistantDelta{Type: "toolcall_delta", Delta: tc.Function.Arguments, ToolCallID: call.ID, ToolName: call.Name, Partial: *acc})
		}
	}
	if d.FunctionCall != nil {
		const idx = 0
		// function_call is the deprecated single-call shape and has no call ID
		// in the wire contract; keep a stable internal ID for tool-result replay.
		appendToolCallDelta(acc, "call_completion_0", "", d.FunctionCall.Name, d.FunctionCall.Arguments, idx)
		call := completionToolCallAt(acc, idx)
		if call != nil && d.FunctionCall.Arguments != "" {
			deltas = append(deltas, loop.AssistantDelta{Type: "toolcall_delta", Delta: d.FunctionCall.Arguments, ToolCallID: call.ID, ToolName: call.Name, Partial: *acc})
		}
	}
	switch chunk.Choices[0].FinishReason {
	case "tool_calls":
		if err := validateCompletionsOutput(acc); err != nil {
			return sseParseResult{err: err}
		}
		acc.StopReason = "toolUse"
	case "function_call":
		if err := validateCompletionsOutput(acc); err != nil {
			return sseParseResult{err: err}
		}
		acc.StopReason = "toolUse"
	case "length":
		acc.StopReason = "length"
	case "stop":
		acc.StopReason = "stop"
	case "content_filter":
		acc.StopReason = "error"
		acc.ErrorMessage = "OpenAI completion stopped by content filter"
	}
	return sseParseResult{deltas: deltas}
}

func completionToolCallAt(m *types.Message, index int) *types.Content {
	if index < 0 {
		return nil
	}
	position := 0
	for i := range m.Content {
		if m.Content[i].Type != "toolCall" {
			continue
		}
		if position == index {
			return &m.Content[i]
		}
		position++
	}
	return nil
}

func validateCompletionsRequest(req loop.Request) error {
	for _, message := range replayable(req.Messages) {
		switch message.Role {
		case "toolResult":
			if message.ToolCallID == "" {
				return errors.New("Chat Completions tool message has no tool_call_id")
			}
		case "assistant":
			for _, block := range message.Content {
				if block.Type != "toolCall" {
					continue
				}
				if block.ToolType == "custom" {
					return errors.New("Chat Completions does not support custom tool calls")
				}
				if block.ID == "" || block.Name == "" {
					return errors.New("Chat Completions assistant tool call has no id or name")
				}
			}
		}
	}
	return nil
}

func validateCompletionsOutput(acc *types.Message) error {
	for _, call := range acc.ToolCalls() {
		if call.ID == "" || call.Name == "" {
			return errors.New("Chat Completions tool call is missing id or name")
		}
		if call.ArgumentsRaw == "" {
			if call.Arguments == nil {
				return fmt.Errorf("Chat Completions tool call %q has no arguments", call.ID)
			}
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(call.ArgumentsRaw), &args); err != nil {
			return fmt.Errorf("Chat Completions tool call %q has invalid JSON arguments: %w", call.ID, err)
		}
		if args == nil {
			return fmt.Errorf("Chat Completions tool call %q arguments must be a JSON object", call.ID)
		}
	}
	return nil
}
