package llmprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// AnthropicBody is the Anthropic Messages payload.
func AnthropicBody(req Request) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages))
	// Combine consecutive toolResults into one user message containing multiple
	// tool_result blocks. Anthropic permits images in each tool_result.content,
	// so do not split them into trailing user messages.
	history := Replayable(req.Messages)
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
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}
	return body
}

func anthropicToolResultBlock(m Message) map[string]any {
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

func toAnthropicMessage(m Message) map[string]any {
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
				if c.Thinking == "" && c.ThinkingSignature == "" && c.ThinkingData == "" {
					continue
				}
				if c.ThinkingData != "" {
					blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": c.ThinkingData})
					continue
				}
				block := map[string]any{"type": "thinking", "thinking": c.Thinking}
				if c.ThinkingSignature != "" {
					block["signature"] = c.ThinkingSignature
				}
				blocks = append(blocks, block)
			case "toolCall":
				input := c.Arguments
				if rawJSON := validObjectArgumentsRaw(c.ArgumentsRaw); rawJSON != "" {
					var rawInput map[string]any
					_ = json.Unmarshal([]byte(rawJSON), &rawInput)
					input = rawInput
				}
				if input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    c.ID,
					"name":  c.Name,
					"input": input,
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

func anthropicMediaBlocks(m Message) []map[string]any {
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

func (l *Client) streamAnthropic(ctx context.Context, req Request, emit func(AssistantDelta) error) (Message, error) {
	if err := validateAnthropicRequest(req); err != nil {
		return Message{Role: "assistant", StopReason: "error", ErrorMessage: err.Error()}, err
	}
	url := l.Base + "/v1/messages"
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Api-Key", l.APIKey)
	h.Set("Anthropic-Version", "2023-06-01")
	return l.postStream(ctx, url, AnthropicBody(req), h, emit, parseAnthropicSSE, true)
}

func parseAnthropicSSE(event, data string, acc *Message) sseParseResult {
	var obj map[string]any
	if json.Unmarshal([]byte(data), &obj) != nil {
		return sseParseResult{err: errors.New("invalid Anthropic SSE JSON")}
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		typ = event
	}
	switch typ {
	case "error":
		errorMessage := "Anthropic stream failed"
		if e, ok := obj["error"].(map[string]any); ok {
			if message, _ := e["message"].(string); message != "" {
				errorMessage = message
			}
		} else if message, _ := obj["message"].(string); message != "" {
			errorMessage = message
		}
		acc.StopReason = "error"
		acc.ErrorMessage = errorMessage
		return sseParseResult{terminal: true}

	case "content_block_delta":
		index := anthropicStreamIndex(obj)
		if index < 0 {
			return sseParseResult{err: errors.New("Anthropic content block delta has no index")}
		}
		delta, ok := obj["delta"].(map[string]any)
		if !ok {
			return sseParseResult{err: errors.New("Anthropic content block delta has no delta object")}
		}
		switch t, _ := delta["type"].(string); t {
		case "text_delta":
			text, _ := delta["text"].(string)
			block, err := anthropicExistingContentBlock(acc, index, "text")
			if err != nil {
				return sseParseResult{err: err}
			}
			block.Text += text
			return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: text, Partial: *acc}, emit: text != ""}
		case "thinking_delta":
			thinking, _ := delta["thinking"].(string)
			block, err := anthropicExistingContentBlock(acc, index, "thinking")
			if err != nil {
				return sseParseResult{err: err}
			}
			block.Thinking += thinking
			return sseParseResult{delta: AssistantDelta{Type: "thinking_delta", Delta: thinking, Partial: *acc}, emit: thinking != ""}
		case "signature_delta":
			signature, _ := delta["signature"].(string)
			block, err := anthropicExistingContentBlock(acc, index, "thinking")
			if err != nil {
				return sseParseResult{err: err}
			}
			block.ThinkingSignature += signature
		case "input_json_delta":
			partial, _ := delta["partial_json"].(string)
			block, err := anthropicExistingContentBlock(acc, index, "toolCall")
			if err != nil {
				return sseParseResult{err: err}
			}
			appendToolArgs(block, partial)
		case "":
			return sseParseResult{err: errors.New("Anthropic content block delta has no type")}
		}

	case "content_block_start":
		index := anthropicStreamIndex(obj)
		if index < 0 {
			return sseParseResult{err: errors.New("Anthropic content block start has no index")}
		}
		blk, ok := obj["content_block"].(map[string]any)
		if !ok {
			return sseParseResult{err: errors.New("Anthropic content block start has no content_block")}
		}
		t, _ := blk["type"].(string)
		// Anthropic's index is the identity of a content block. Rejecting a
		// duplicate start prevents a retry or malformed stream from silently
		// overwriting already accumulated thinking/tool state.
		if anthropicContentBlockAt(acc, index) != nil {
			return sseParseResult{err: fmt.Errorf("Anthropic content block %d started more than once", index)}
		}
		switch t {
		case "text":
			block, err := anthropicContentBlock(acc, index, "text")
			if err != nil {
				return sseParseResult{err: err}
			}
			block.Text, _ = blk["text"].(string)
			if block.Text != "" {
				return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: block.Text, Partial: *acc}, emit: true}
			}
		case "thinking":
			block, err := anthropicContentBlock(acc, index, "thinking")
			if err != nil {
				return sseParseResult{err: err}
			}
			block.Thinking, _ = blk["thinking"].(string)
			block.ThinkingSignature, _ = blk["signature"].(string)
			if block.Thinking != "" {
				return sseParseResult{delta: AssistantDelta{Type: "thinking_delta", Delta: block.Thinking, Partial: *acc}, emit: true}
			}
		case "redacted_thinking":
			block, err := anthropicContentBlock(acc, index, "thinking")
			if err != nil {
				return sseParseResult{err: err}
			}
			block.ThinkingData, _ = blk["data"].(string)
			if block.ThinkingData == "" {
				return sseParseResult{err: errors.New("Anthropic redacted_thinking has no data")}
			}
		case "tool_use":
			id, _ := blk["id"].(string)
			name, _ := blk["name"].(string)
			if id == "" || name == "" {
				return sseParseResult{err: errors.New("Anthropic tool_use is missing id or name")}
			}
			block, err := anthropicContentBlock(acc, index, "toolCall")
			if err != nil {
				return sseParseResult{err: err}
			}
			block.ID, block.Name = id, name
			block.Arguments = map[string]any{}
			input, exists := blk["input"]
			if !exists || input == nil {
				return sseParseResult{err: errors.New("Anthropic tool_use has no input")}
			}
			value, valid := input.(map[string]any)
			if !valid {
				return sseParseResult{err: errors.New("Anthropic tool_use input must be an object")}
			}
			block.Arguments = value
			if len(value) > 0 {
				block.ArgumentsRaw = marshalArguments(value)
				return sseParseResult{delta: AssistantDelta{
					Type: "toolcall_delta", Delta: block.ArgumentsRaw,
					ToolCallID: block.ID, ToolName: block.Name, Partial: *acc,
				}, emit: true}
			}
		default:
			return sseParseResult{err: fmt.Errorf("unsupported Anthropic content block type %q", t)}
		}

	case "content_block_stop":
		index := anthropicStreamIndex(obj)
		if index < 0 {
			return sseParseResult{err: errors.New("Anthropic content block stop has no index")}
		}
		if anthropicContentBlockAt(acc, index) == nil {
			return sseParseResult{err: fmt.Errorf("Anthropic content block stop references unknown index %d", index)}
		}

	case "message_delta":
		if usage, ok := obj["usage"].(map[string]any); ok {
			mergeAnthropicUsage(acc, usage)
		}
		if delta, ok := obj["delta"].(map[string]any); ok {
			applyAnthropicStopReason(acc, stringValue(delta["stop_reason"]))
		}

	case "message_start":
		msg, ok := obj["message"].(map[string]any)
		if !ok {
			return sseParseResult{err: errors.New("Anthropic message_start has no message object")}
		}
		acc.ResponseID = stringValue(msg["id"])
		if acc.ResponseID == "" {
			return sseParseResult{err: errors.New("Anthropic message_start has empty message id")}
		}
		if usage, ok := msg["usage"].(map[string]any); ok {
			mergeAnthropicUsage(acc, usage)
		}

	case "message_stop":
		if err := validateAnthropicOutput(acc); err != nil {
			acc.StopReason = "error"
			acc.ErrorMessage = err.Error()
		}
		return sseParseResult{terminal: true}
	}
	return sseParseResult{}
}

func anthropicStreamIndex(obj map[string]any) int {
	value, ok := obj["index"]
	if !ok {
		return -1
	}
	return asInt(value)
}

func anthropicContentBlockAt(m *Message, index int) *Content {
	for i := range m.Content {
		if m.Content[i].StreamIndex == index {
			return &m.Content[i]
		}
	}
	return nil
}

func anthropicExistingContentBlock(m *Message, index int, typ string) (*Content, error) {
	block := anthropicContentBlockAt(m, index)
	if block == nil {
		// Do not recreate a block from a delta: the protocol requires start →
		// delta ordering, and recreating here would associate a late delta with
		// the wrong logical output item.
		return nil, fmt.Errorf("Anthropic content block delta references unknown index %d", index)
	}
	if block.Type != typ {
		return nil, fmt.Errorf("Anthropic content block %d changed type from %s to %s", index, block.Type, typ)
	}
	return block, nil
}

func anthropicContentBlock(m *Message, index int, typ string) (*Content, error) {
	if block := anthropicContentBlockAt(m, index); block != nil {
		if block.Type != "" && block.Type != typ {
			return nil, fmt.Errorf("Anthropic content block %d changed type from %s to %s", index, block.Type, typ)
		}
		block.Type = typ
		return block, nil
	}
	block := Content{Type: typ, StreamIndex: index}
	position := len(m.Content)
	for i := range m.Content {
		if m.Content[i].StreamIndex > index {
			position = i
			break
		}
	}
	m.Content = append(m.Content, Content{})
	copy(m.Content[position+1:], m.Content[position:])
	m.Content[position] = block
	return &m.Content[position], nil
}

func mergeAnthropicUsage(acc *Message, usage map[string]any) {
	if acc.Usage == nil {
		acc.Usage = &Usage{}
	}
	if value, ok := usage["input_tokens"]; ok {
		acc.Usage.Input = asInt(value)
	}
	if value, ok := usage["output_tokens"]; ok {
		acc.Usage.Output = asInt(value)
	}
	if value, ok := usage["cache_read_input_tokens"]; ok {
		acc.Usage.CacheRead = asInt(value)
	}
	if value, ok := usage["cache_creation_input_tokens"]; ok {
		acc.Usage.CacheWrite = asInt(value)
	}
}

func applyAnthropicStopReason(acc *Message, reason string) {
	switch reason {
	case "tool_use":
		acc.StopReason = "toolUse"
	case "max_tokens":
		acc.StopReason = "length"
	case "end_turn", "stop_sequence":
		acc.StopReason = "stop"
	case "pause_turn":
		// This client has no resumable pause state; preserve the completed stream
		// as a normal stop rather than retrying a request that may have side effects.
		acc.StopReason = "stop"
	case "refusal", "model_context_window_exceeded":
		acc.StopReason = "error"
		acc.ErrorMessage = "Anthropic response stopped: " + reason
	}
}

func validateAnthropicRequest(req Request) error {
	for _, message := range Replayable(req.Messages) {
		if message.Role == "toolResult" {
			if message.ToolCallID == "" {
				return errors.New("Anthropic tool_result has no tool_use_id")
			}
			continue
		}
		if message.Role != "assistant" {
			continue
		}
		for _, block := range message.Content {
			switch block.Type {
			case "thinking":
				if block.ThinkingData == "" && block.ThinkingSignature == "" && block.Thinking != "" {
					return errors.New("Anthropic thinking block has no signature")
				}
			case "toolCall":
				if block.ID == "" || block.Name == "" {
					return errors.New("Anthropic tool_use has no id or name")
				}
				if block.Arguments == nil {
					if block.ArgumentsRaw == "" {
						return fmt.Errorf("Anthropic tool_use %q input is not an object", block.ID)
					}
					if validObjectArgumentsRaw(block.ArgumentsRaw) == "" {
						// A length-truncated tool call is followed by an error
						// tool_result. Its partial raw text cannot be replayed as
						// Anthropic input, so the request encoder will send {}.
						if message.StopReason != "length" {
							return fmt.Errorf("Anthropic tool_use %q input is not an object", block.ID)
						}
					}
				}
			}
		}
	}
	return nil
}

func validateAnthropicOutput(acc *Message) error {
	for i := range acc.Content {
		block := &acc.Content[i]
		if block.Type != "toolCall" {
			continue
		}
		if block.ID == "" || block.Name == "" {
			return errors.New("Anthropic tool_use stream is missing id or name")
		}
		if block.ArgumentsRaw == "" {
			if block.Arguments == nil {
				block.Arguments = map[string]any{}
			}
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(block.ArgumentsRaw), &args); err != nil {
			return fmt.Errorf("Anthropic tool_use %q input is invalid JSON: %w", block.ID, err)
		}
		if args == nil {
			return fmt.Errorf("Anthropic tool_use %q input is not an object", block.ID)
		}
	}
	return nil
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}
