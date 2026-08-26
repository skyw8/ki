package llmprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ResponsesBody is the OpenAI Responses API payload. The request is replayed
// statelessly, so reasoning items with encrypted content must be requested and
// retained as input items on the next turn.
func ResponsesBody(req Request) map[string]any {
	input := make([]any, 0, len(req.Messages))
	for _, m := range Replayable(req.Messages) {
		input = append(input, toResponsesItems(m)...)
	}
	body := map[string]any{
		"model":   req.Model,
		"input":   input,
		"stream":  true,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if effort := mappedThinking(req); effort != "" {
		body["reasoning"] = map[string]any{"effort": effort}
	}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			if t.Type == "custom" {
				tool := map[string]any{"type": "custom", "name": t.Name, "description": t.Description}
				if t.Format != nil {
					tool["format"] = t.Format
				}
				tools = append(tools, tool)
				continue
			}
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			})
		}
		body["tools"] = tools
	}
	return body
}

func toResponsesItems(m Message) []any {
	switch m.Role {
	case "toolResult":
		// Responses keeps each tool result paired with its function/custom call.
		// Images belong inside output rather than in a separate user message.
		outputType := "function_call_output"
		if m.ToolType == "custom" {
			outputType = "custom_tool_call_output"
		}
		return []any{map[string]any{
			"type":    outputType,
			"call_id": m.ToolCallID,
			"output":  responsesToolOutput(m),
		}}
	case "assistant":
		var items []any
		var text strings.Builder
		var textSignature string
		flushText := func() {
			if text.Len() == 0 {
				return
			}
			item := map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": text.String(), "annotations": []any{}}},
			}
			if meta := responseOpaqueItem(textSignature, "message"); meta != nil {
				if id, ok := meta["id"].(string); ok && id != "" {
					item["id"] = id
				}
				if phase, ok := meta["phase"].(string); ok && phase != "" {
					item["phase"] = phase
				}
			}
			items = append(items, item)
			text.Reset()
			textSignature = ""
		}
		for _, c := range m.Content {
			switch c.Type {
			case "text", "":
				if text.Len() == 0 {
					textSignature = c.TextSignature
				}
				text.WriteString(c.Text)
			case "thinking":
				flushText()
				if item := responseOpaqueItem(c.ThinkingSignature, "reasoning"); item != nil {
					items = append(items, item)
				}
			case "toolCall":
				flushText()
				item := map[string]any{"call_id": c.ID, "name": c.Name}
				if c.ToolType == "custom" {
					item["type"] = "custom_tool_call"
					item["input"] = c.Input
				} else {
					item["type"] = "function_call"
					arguments := validObjectArgumentsRaw(c.ArgumentsRaw)
					if arguments == "" {
						input := c.Arguments
						if input == nil {
							input = map[string]any{}
						}
						arguments = marshalArguments(input)
					}
					item["arguments"] = arguments
				}
				if c.ItemID != "" {
					item["id"] = c.ItemID
				}
				items = append(items, item)
			}
		}
		flushText()
		return items
	default:
		if content := responsesUserContent(m); content != nil {
			return []any{map[string]any{
				"type":    "message",
				"role":    "user",
				"content": content,
			}}
		}
		return []any{map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": m.Text()}},
		}}
	}
}

func responseOpaqueItem(signature, expectedType string) map[string]any {
	if signature == "" {
		return nil
	}
	var item map[string]any
	if json.Unmarshal([]byte(signature), &item) != nil || item == nil {
		return nil
	}
	if expectedType != "" {
		if typ, _ := item["type"].(string); typ != expectedType {
			return nil
		}
	}
	return item
}

func validateResponsesBody(body map[string]any) error {
	input, ok := body["input"].([]any)
	if !ok {
		return nil
	}
	for i, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("responses input[%d] is not an object", i)
		}
		typ, _ := item["type"].(string)
		switch typ {
		case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
			callID, _ := item["call_id"].(string)
			if strings.TrimSpace(callID) == "" {
				return fmt.Errorf("responses input[%d] %s has empty call_id", i, typ)
			}
		}
	}
	return nil
}

func responsesToolOutput(m Message) any {
	text := m.Text()
	imgs := responsesImageParts(m)
	if len(imgs) == 0 {
		if text == "" {
			return "(no tool output)"
		}
		return text
	}
	var out []map[string]any
	if text == "" {
		text = "(see attached image)"
	}
	out = append(out, map[string]any{"type": "input_text", "text": text})
	out = append(out, imgs...)
	return out
}

func responsesUserContent(m Message) []map[string]any {
	var content []map[string]any
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
			content = append(content, map[string]any{
				"type":      "input_image",
				"image_url": "data:" + mime + ";base64," + c.Data,
			})
		case "text", "":
			if c.Text != "" {
				content = append(content, map[string]any{"type": "input_text", "text": c.Text})
			}
		}
	}
	if !hasMedia {
		return nil
	}
	return content
}

func responsesImageParts(m Message) []map[string]any {
	c := responsesUserContent(m)
	if c == nil {
		return nil
	}
	var imgs []map[string]any
	for _, p := range c {
		if p["type"] == "input_image" {
			imgs = append(imgs, p)
		}
	}
	return imgs
}

func (l *Client) streamResponses(ctx context.Context, req Request, emit func(AssistantDelta) error) (Message, error) {
	body := ResponsesBody(req)
	if err := validateResponsesBody(body); err != nil {
		return Message{Role: "assistant", StopReason: "error", ErrorMessage: err.Error()}, err
	}
	url := l.Base + "/responses"
	return l.postStream(ctx, url, body, l.oaHeaders(), emit, parseResponsesSSE, true)
}

func parseResponsesSSE(event, data string, acc *Message) sseParseResult {
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return sseParseResult{err: errors.New("invalid Responses SSE JSON")}
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		typ = event
	}
	switch typ {
	case "response.created", "response.in_progress", "response.queued":
		setResponseID(acc, obj)
		return sseParseResult{}
	case "response.output_item.added", "response.output_item.done":
		item, ok := obj["item"].(map[string]any)
		if !ok {
			return sseParseResult{err: errors.New("Responses output item event has no item")}
		}
		return applyResponsesOutputItem(acc, item)
	case "response.content_part.added", "response.content_part.done":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		part, ok := obj["part"].(map[string]any)
		if !ok {
			return sseParseResult{err: errors.New("Responses content part event has no part")}
		}
		return applyResponsesOutputItem(acc, map[string]any{
			"type":    "message",
			"id":      itemID,
			"content": []any{part},
		})
	case "response.output_text.delta":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["delta"].(string)
		appendResponseText(acc, itemID, text)
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: text, Partial: *acc}, emit: text != ""}
	case "response.output_text.done":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["text"].(string)
		delta := mergeResponseText(acc, itemID, text)
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: delta, Partial: *acc}, emit: delta != ""}
	case "response.refusal.delta":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["delta"].(string)
		appendResponseText(acc, itemID, text)
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: text, Partial: *acc}, emit: text != ""}
	case "response.refusal.done":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["refusal"].(string)
		delta := mergeResponseText(acc, itemID, text)
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: delta, Partial: *acc}, emit: delta != ""}
	case "response.reasoning_summary_part.added":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		part, _ := obj["part"].(map[string]any)
		text, _ := part["text"].(string)
		appendResponseThinking(acc, itemID, text)
		return sseParseResult{delta: AssistantDelta{Type: "thinking_delta", Delta: text, Partial: *acc}, emit: text != ""}
	case "response.reasoning_summary_part.done":
		return sseParseResult{}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["delta"].(string)
		appendResponseThinking(acc, itemID, text)
		return sseParseResult{delta: AssistantDelta{Type: "thinking_delta", Delta: text, Partial: *acc}, emit: text != ""}
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		return sseParseResult{}
	case "response.function_call_arguments.delta":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		delta, _ := obj["delta"].(string)
		callID, _ := obj["call_id"].(string)
		name, _ := obj["name"].(string)
		c := findOrAddToolCall(acc, callID, itemID, name, -1)
		appendToolArgs(c, delta)
		return sseParseResult{delta: AssistantDelta{Type: "toolcall_delta", Delta: delta, ToolCallID: c.ID, ToolName: c.Name, Partial: *acc}, emit: delta != ""}
	case "response.function_call_arguments.done":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		callID, _ := obj["call_id"].(string)
		name, _ := obj["name"].(string)
		c := findOrAddToolCall(acc, callID, itemID, name, -1)
		args, _ := obj["arguments"].(string)
		if err := setResponseFunctionArguments(c, args); err != nil {
			return sseParseResult{err: err}
		}
		if c.ID == "" {
			return sseParseResult{err: errors.New("Responses function call has empty call_id")}
		}
		acc.StopReason = "toolUse"
		return sseParseResult{}
	case "response.custom_tool_call_input.delta":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		delta, _ := obj["delta"].(string)
		callID, _ := obj["call_id"].(string)
		name, _ := obj["name"].(string)
		c := findOrAddToolCall(acc, callID, itemID, name, -1)
		c.ToolType = "custom"
		c.Input += delta
		return sseParseResult{delta: AssistantDelta{Type: "custom_tool_call_input_delta", Delta: delta, ToolCallID: c.ID, ToolName: c.Name, Partial: *acc}, emit: delta != ""}
	case "response.custom_tool_call_input.done":
		itemID, err := requiredResponseItemID(obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		callID, _ := obj["call_id"].(string)
		name, _ := obj["name"].(string)
		c := findOrAddToolCall(acc, callID, itemID, name, -1)
		c.ToolType = "custom"
		if input, ok := obj["input"].(string); ok {
			c.Input = input
		}
		if c.ID == "" {
			return sseParseResult{err: errors.New("Responses custom tool call has empty call_id")}
		}
		acc.StopReason = "toolUse"
		return sseParseResult{}
	case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "error":
		response := responseObject(obj)
		setResponseID(acc, obj)
		if output, ok := response["output"].([]any); ok {
			for _, raw := range output {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				result := applyResponsesOutputItem(acc, item)
				if result.err != nil {
					return result
				}
			}
		}
		applyResponsesUsage(acc, response)
		status, _ := response["status"].(string)
		switch {
		case typ == "response.failed" || typ == "error" || status == "failed":
			acc.StopReason = "error"
			acc.ErrorMessage = responseErrorMessage(obj, response)
		case typ == "response.cancelled" || status == "cancelled":
			acc.StopReason = "aborted"
		case typ == "response.incomplete" || status == "incomplete":
			incomplete, _ := response["incomplete_details"].(map[string]any)
			if reason, _ := incomplete["reason"].(string); reason == "max_output_tokens" {
				acc.StopReason = "length"
			} else {
				acc.StopReason = "error"
				acc.ErrorMessage = responseErrorMessage(obj, response)
				if acc.ErrorMessage == "" {
					acc.ErrorMessage = "Responses response was incomplete"
				}
			}
		default:
			if len(acc.ToolCalls()) > 0 {
				acc.StopReason = "toolUse"
			} else {
				acc.StopReason = "stop"
			}
		}
		return sseParseResult{terminal: true}
	}
	return sseParseResult{}
}

func responseObject(obj map[string]any) map[string]any {
	if response, ok := obj["response"].(map[string]any); ok {
		return response
	}
	return obj
}

func responseErrorMessage(obj, response map[string]any) string {
	for _, source := range []map[string]any{response, obj} {
		if errObj, ok := source["error"].(map[string]any); ok {
			if message, _ := errObj["message"].(string); message != "" {
				return message
			}
		}
		if message, _ := source["message"].(string); message != "" {
			return message
		}
	}
	return "Responses request failed"
}

func setResponseID(acc *Message, obj map[string]any) {
	response := responseObject(obj)
	if id, _ := response["id"].(string); id != "" {
		acc.ResponseID = id
	}
}

func requiredResponseItemID(obj map[string]any) (string, error) {
	itemID, _ := obj["item_id"].(string)
	if strings.TrimSpace(itemID) == "" {
		return "", errors.New("Responses stream event has empty item_id")
	}
	return itemID, nil
}

func applyResponsesOutputItem(acc *Message, item map[string]any) sseParseResult {
	itemType, _ := item["type"].(string)
	itemID, _ := item["id"].(string)
	switch itemType {
	case "message":
		if itemID == "" {
			return sseParseResult{err: errors.New("Responses message output item has empty id")}
		}
		var delta strings.Builder
		for _, raw := range responseContent(item["content"]) {
			contentType, _ := raw["type"].(string)
			if contentType != "output_text" && contentType != "refusal" {
				continue
			}
			text, _ := raw["text"].(string)
			if contentType == "refusal" {
				text, _ = raw["refusal"].(string)
			}
			if text != "" {
				delta.WriteString(mergeResponseText(acc, itemID, text))
			}
		}
		block := responseTextBlock(acc, itemID)
		meta := map[string]any{"v": 1, "type": "message", "id": itemID}
		if phase, _ := item["phase"].(string); phase != "" {
			meta["phase"] = phase
		}
		block.TextSignature = marshalArguments(meta)
		textDelta := delta.String()
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: textDelta, Partial: *acc}, emit: textDelta != ""}
	case "function_call", "custom_tool_call":
		if itemID == "" {
			return sseParseResult{err: fmt.Errorf("Responses %s has empty id", itemType)}
		}
		callID, _ := item["call_id"].(string)
		if strings.TrimSpace(callID) == "" {
			return sseParseResult{err: fmt.Errorf("Responses %s has empty call_id", itemType)}
		}
		name, _ := item["name"].(string)
		c := findOrAddToolCall(acc, callID, itemID, name, -1)
		if itemType == "custom_tool_call" {
			c.ToolType = "custom"
			if input, ok := item["input"].(string); ok {
				c.Input = input
			}
		} else if args, ok := item["arguments"].(string); ok && args != "" {
			if err := setResponseFunctionArguments(c, args); err != nil {
				return sseParseResult{err: err}
			}
		}
		return sseParseResult{}
	case "reasoning":
		if itemID == "" {
			return sseParseResult{err: errors.New("Responses reasoning output item has empty id")}
		}
		block := responseThinkingBlock(acc, itemID)
		for _, raw := range responseContent(item["summary"]) {
			if text, _ := raw["text"].(string); text != "" {
				mergeResponseThinking(block, text)
			}
		}
		for _, raw := range responseContent(item["content"]) {
			if text, _ := raw["text"].(string); text != "" {
				mergeResponseThinking(block, text)
			}
		}
		if item["encrypted_content"] != nil {
			block.ThinkingSignature = marshalArguments(item)
		}
		return sseParseResult{}
	default:
		return sseParseResult{}
	}
}

func responseContent(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, raw := range list {
		if item, ok := raw.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

func responseTextBlock(acc *Message, itemID string) *Content {
	for i := range acc.Content {
		if acc.Content[i].Type == "text" && (itemID == "" || acc.Content[i].ItemID == itemID) {
			if itemID != "" {
				acc.Content[i].ItemID = itemID
			}
			return &acc.Content[i]
		}
	}
	acc.Content = append(acc.Content, Content{Type: "text", ItemID: itemID})
	return &acc.Content[len(acc.Content)-1]
}

func appendResponseText(acc *Message, itemID, delta string) {
	if delta == "" {
		return
	}
	responseTextBlock(acc, itemID).Text += delta
}

func mergeResponseText(acc *Message, itemID, text string) string {
	if text == "" {
		return ""
	}
	block := responseTextBlock(acc, itemID)
	if block.Text == text {
		return ""
	}
	if block.Text == "" {
		block.Text = text
		return text
	}
	if strings.HasPrefix(text, block.Text) {
		delta := strings.TrimPrefix(text, block.Text)
		block.Text = text
		return delta
	}
	// A final event can repair a partial stream. The final message is the
	// source of truth, but replaying the replacement as a delta would duplicate
	// text in clients that already rendered earlier fragments.
	block.Text = text
	return ""
}

func responseThinkingBlock(acc *Message, itemID string) *Content {
	for i := range acc.Content {
		if acc.Content[i].Type == "thinking" && (itemID == "" || acc.Content[i].ItemID == itemID) {
			if itemID != "" {
				acc.Content[i].ItemID = itemID
			}
			return &acc.Content[i]
		}
	}
	acc.Content = append(acc.Content, Content{Type: "thinking", ItemID: itemID})
	return &acc.Content[len(acc.Content)-1]
}

func appendResponseThinking(acc *Message, itemID, delta string) {
	if delta == "" {
		return
	}
	responseThinkingBlock(acc, itemID).Thinking += delta
}

func mergeResponseThinking(block *Content, text string) {
	if block == nil || text == "" {
		return
	}
	if block.Thinking == "" || strings.HasPrefix(text, block.Thinking) {
		block.Thinking = text
	}
}

func setResponseFunctionArguments(c *Content, raw string) error {
	if c == nil {
		return errors.New("Responses function call is missing")
	}
	if raw != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return fmt.Errorf("invalid Responses function call arguments: %w", err)
		}
		if args == nil {
			return errors.New("Responses function call arguments must be a JSON object")
		}
		c.Arguments = args
	}
	c.ArgumentsRaw = raw
	return nil
}

func applyResponsesUsage(acc *Message, response map[string]any) {
	usage, ok := response["usage"].(map[string]any)
	if !ok {
		return
	}
	details, _ := usage["input_tokens_details"].(map[string]any)
	cached := asInt(usage["cached_tokens"])
	if details != nil {
		cached = asInt(details["cached_tokens"])
	}
	cacheWrite := 0
	if details != nil {
		cacheWrite = asInt(details["cache_write_tokens"])
	}
	if cacheWrite == 0 {
		cacheWrite = asInt(usage["cache_write_tokens"])
	}
	acc.Usage = &Usage{
		Input:       asInt(usage["input_tokens"]),
		Output:      asInt(usage["output_tokens"]),
		CacheRead:   cached,
		CacheWrite:  cacheWrite,
		TotalTokens: asInt(usage["total_tokens"]),
	}
}
