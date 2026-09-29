package llmprotocol

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesBody is the OpenAI Responses API payload. The request is replayed
// statelessly, so reasoning items with encrypted content must be requested and
// retained as input items on the next turn.
func ResponsesBody(req Request) map[string]any {
	input := responsesInput(req.ResponsesWindow, req.Messages)
	body := map[string]any{
		"model":   req.Model,
		"input":   input,
		"stream":  true,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
	}
	if req.ResponsesCompactThreshold > 0 {
		body["context_management"] = []map[string]any{{
			"type":              "compaction",
			"compact_threshold": req.ResponsesCompactThreshold,
		}}
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

func responsesInput(window ResponsesWindow, messages []Message) []any {
	input := make([]any, 0, len(window)+len(messages))
	for _, item := range window {
		input = append(input, item)
	}
	for _, m := range Replayable(messages) {
		items := toResponsesItems(m)
		// A server-side compaction item supersedes every earlier stateless
		// input item. Reset only for items emitted by an ordinary response;
		// standalone /responses/compact windows remain canonical and unpruned.
		if len(m.ResponsesItems) > 0 {
			input = input[:0]
		}
		input = append(input, items...)
	}
	return input
}

// ResponsesItemsForMessage converts one message to the exact ordered Responses
// items used for stateless replay. It is used by hosts to turn a server-side
// compaction response into a durable canonical checkpoint.
func ResponsesItemsForMessage(message Message) (ResponsesWindow, error) {
	if len(message.ResponsesItems) > 0 {
		out := make(ResponsesWindow, len(message.ResponsesItems))
		for i, item := range message.ResponsesItems {
			out[i] = append(ResponsesItem(nil), item...)
		}
		return out, nil
	}
	rawItems := toResponsesItems(message)
	out := make(ResponsesWindow, 0, len(rawItems))
	for _, value := range rawItems {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("marshal Responses item: %w", err)
		}
		var item ResponsesItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
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
		if len(m.ResponsesItems) > 0 {
			items := make([]any, len(m.ResponsesItems))
			for i, item := range m.ResponsesItems {
				items[i] = item
			}
			return items
		}
		items := make([]any, 0, len(m.ResponsesItems)+len(m.Content))
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
		item, err := responsesInputObject(raw)
		if err != nil {
			return fmt.Errorf("responses input[%d]: %w", i, errResponsesInputNotObject)
		}
		typ, _ := item["type"].(string)
		switch typ {
		case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
			callID, _ := item["call_id"].(string)
			if strings.TrimSpace(callID) == "" {
				return fmt.Errorf("responses input[%d] %s: %w", i, typ, errResponsesInputEmptyCallID)
			}
		}
	}
	return nil
}

func responsesInputObject(raw any) (map[string]any, error) {
	if item, ok := raw.(map[string]any); ok {
		return item, nil
	}
	var data []byte
	switch item := raw.(type) {
	case ResponsesItem:
		data = item
	case json.RawMessage:
		data = item
	default:
		return nil, errResponsesInputNotObject
	}
	if err := validateResponsesItem(data); err != nil {
		return nil, err
	}
	var item map[string]any
	if err := json.Unmarshal(data, &item); err != nil || item == nil {
		return nil, errResponsesInputNotObject
	}
	return item, nil
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
			mime := cmp.Or(c.MIMEType, "image/png")
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

// ResponsesCompactBody builds the wire payload for POST /responses/compact.
// The provider-owned window is retained in front of newly converted messages.
func ResponsesCompactBody(req ResponsesCompactRequest) map[string]any {
	body := map[string]any{
		"model": req.Model,
		"input": responsesInput(req.Window, req.Messages),
	}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	return body
}

// CompactResponses invokes OpenAI's standalone Responses compaction endpoint.
// Output is the complete canonical next context window and must be replayed
// without pruning.
func (l *Client) CompactResponses(ctx context.Context, req ResponsesCompactRequest) (ResponsesCompactResult, error) {
	body := ResponsesCompactBody(req)
	if err := validateResponsesBody(body); err != nil {
		return ResponsesCompactResult{}, &nonRetryableError{err: err}
	}
	raw, err := l.postJSON(ctx, l.Base+"/responses/compact", body, l.oaHeaders())
	if err != nil {
		return ResponsesCompactResult{}, err
	}
	var response struct {
		ID     string          `json:"id"`
		Output ResponsesWindow `json:"output"`
		Usage  map[string]any  `json:"usage"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return ResponsesCompactResult{}, &nonRetryableError{
			err: fmt.Errorf("%w: %v", errResponsesCompactInvalidJSON, err),
		}
	}
	if len(response.Output) == 0 {
		return ResponsesCompactResult{}, &nonRetryableError{err: errResponsesCompactOutputMissing}
	}
	hasCompaction := false
	for _, item := range response.Output {
		if item.Type() != "compaction" {
			continue
		}
		var header struct {
			EncryptedContent string `json:"encrypted_content"`
		}
		if json.Unmarshal(item, &header) == nil && header.EncryptedContent != "" {
			hasCompaction = true
			break
		}
	}
	if !hasCompaction {
		return ResponsesCompactResult{}, &nonRetryableError{err: errResponsesCompactOutputMissing}
	}
	var usage *Usage
	if response.Usage != nil {
		message := Message{}
		applyResponsesUsage(&message, map[string]any{"usage": response.Usage})
		usage = message.Usage
	}
	return ResponsesCompactResult{
		ID:     response.ID,
		Output: response.Output,
		Usage:  usage,
	}, nil
}

func (l *Client) streamResponses(ctx context.Context, req Request, emit func(AssistantDelta) error) (Message, error) {
	body := ResponsesBody(req)
	if err := validateResponsesBody(body); err != nil {
		return Message{Role: "assistant", StopReason: "error", ErrorMessage: err.Error()}, &nonRetryableError{err: err}
	}
	url := l.Base + "/responses"
	return l.postStream(ctx, url, body, l.oaHeaders(), emit, parseResponsesSSE, true)
}

func parseResponsesSSE(event, data string, acc *Message) sseParseResult {
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return sseParseResult{err: errInvalidResponsesSSEJSON}
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		typ = event
	}
	if window := rawResponsesCompactionWindow([]byte(data), typ); len(window) > 0 {
		acc.ResponsesItems = window
	}
	switch typ {
	case "response.created", "response.in_progress", "response.queued":
		setResponseID(acc, obj)
		return sseParseResult{}
	case "response.output_item.added", "response.output_item.done":
		item, ok := obj["item"].(map[string]any)
		if !ok {
			return sseParseResult{err: errResponsesOutputItemNoItem}
		}
		return applyResponsesOutputItem(acc, item, responseOutputIndex(obj))
	case "response.content_part.added", "response.content_part.done":
		itemID, outputIndex, err := responseTextReference(acc, obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		part, ok := obj["part"].(map[string]any)
		if !ok {
			return sseParseResult{err: errResponsesContentPartNoPart}
		}
		item := map[string]any{"type": "message", "content": []any{part}}
		if itemID != "" {
			item["id"] = itemID
		}
		return applyResponsesOutputItem(acc, item, outputIndex)
	case "response.output_text.delta":
		itemID, outputIndex, err := responseTextReference(acc, obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["delta"].(string)
		appendResponseText(acc, itemID, outputIndex, text)
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: text, Partial: *acc}, emit: text != ""}
	case "response.output_text.done":
		itemID, outputIndex, err := responseTextReference(acc, obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["text"].(string)
		delta := mergeResponseText(acc, itemID, outputIndex, text)
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: delta, Partial: *acc}, emit: delta != ""}
	case "response.refusal.delta":
		itemID, outputIndex, err := responseTextReference(acc, obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["delta"].(string)
		appendResponseText(acc, itemID, outputIndex, text)
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: text, Partial: *acc}, emit: text != ""}
	case "response.refusal.done":
		itemID, outputIndex, err := responseTextReference(acc, obj)
		if err != nil {
			return sseParseResult{err: err}
		}
		text, _ := obj["refusal"].(string)
		delta := mergeResponseText(acc, itemID, outputIndex, text)
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
			return sseParseResult{err: errResponsesFunctionCallEmptyCallID}
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
			return sseParseResult{err: errResponsesCustomToolCallEmptyCallID}
		}
		acc.StopReason = "toolUse"
		return sseParseResult{}
	case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "error":
		response := responseObject(obj)
		setResponseID(acc, obj)
		if output, ok := response["output"].([]any); ok {
			for index, raw := range output {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				outputIndex := responseOutputIndex(item)
				if outputIndex < 0 {
					outputIndex = index
				}
				result := applyResponsesOutputItem(acc, item, outputIndex)
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
	itemID := responseItemID(obj)
	if strings.TrimSpace(itemID) == "" {
		return "", errResponsesStreamEmptyItemID
	}
	return itemID, nil
}

func responseItemID(obj map[string]any) string {
	itemID, _ := obj["item_id"].(string)
	return strings.TrimSpace(itemID)
}

func responseOutputIndex(obj map[string]any) int {
	value, ok := obj["output_index"]
	if !ok {
		return -1
	}
	switch n := value.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		value, err := n.Int64()
		if err == nil {
			return int(value)
		}
	}
	return -1
}

// responseTextReference accepts item_id as the normal identity, but also
// correlates text by output_index or the sole open text item. Some compatible
// gateways omit message item ids while still returning the text stream.
func responseTextReference(acc *Message, obj map[string]any) (string, int, error) {
	itemID := responseItemID(obj)
	outputIndex := responseOutputIndex(obj)
	if itemID != "" || outputIndex >= 0 {
		return itemID, outputIndex, nil
	}
	var candidate *Content
	for i := range acc.Content {
		if acc.Content[i].Type != "text" {
			continue
		}
		if candidate != nil {
			return "", -1, errResponsesTextNoItemOrIndex
		}
		candidate = &acc.Content[i]
	}
	if candidate == nil {
		return "", -1, errResponsesTextNoItemOrIndex
	}
	return candidate.ItemID, candidate.StreamIndex, nil
}

func applyResponsesOutputItem(acc *Message, item map[string]any, outputIndex int) sseParseResult {
	itemType, _ := item["type"].(string)
	itemID, _ := item["id"].(string)
	itemID = strings.TrimSpace(itemID)
	switch itemType {
	case "message":
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
				delta.WriteString(mergeResponseText(acc, itemID, outputIndex, text))
			}
		}
		block := responseTextBlock(acc, itemID, outputIndex)
		if itemID == "" {
			itemID = block.ItemID
		}
		meta := map[string]any{"v": 1, "type": "message"}
		if itemID != "" {
			meta["id"] = itemID
		}
		if phase, _ := item["phase"].(string); phase != "" {
			meta["phase"] = phase
		}
		if itemID != "" || meta["phase"] != nil {
			block.TextSignature = marshalArguments(meta)
		}
		textDelta := delta.String()
		return sseParseResult{delta: AssistantDelta{Type: "text_delta", Delta: textDelta, Partial: *acc}, emit: textDelta != ""}
	case "function_call", "custom_tool_call":
		if itemID == "" {
			return sseParseResult{err: fmt.Errorf("responses %s: %w", itemType, errResponsesItemEmptyID)}
		}
		callID, _ := item["call_id"].(string)
		if strings.TrimSpace(callID) == "" {
			return sseParseResult{err: fmt.Errorf("responses %s: %w", itemType, errResponsesItemEmptyCallID)}
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
			return sseParseResult{err: errResponsesReasoningEmptyID}
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
	case "compaction":
		return sseParseResult{}
	default:
		return sseParseResult{}
	}
}

func rawResponsesCompactionWindow(data []byte, eventType string) ResponsesWindow {
	// Only a completed response defines a canonical next window. An item seen
	// before response.incomplete/failed may be followed by output the provider
	// never committed and must not replace durable context.
	if eventType != "response.completed" && eventType != "response.done" {
		return nil
	}
	var envelope struct {
		Response json.RawMessage   `json:"response"`
		Output   []json.RawMessage `json:"output"`
		Status   string            `json:"status"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return nil
	}
	if envelope.Status != "" && envelope.Status != "completed" {
		return nil
	}
	candidates := envelope.Output
	if len(envelope.Response) > 0 {
		var response struct {
			Status string            `json:"status"`
			Output []json.RawMessage `json:"output"`
		}
		if json.Unmarshal(envelope.Response, &response) != nil ||
			(response.Status != "" && response.Status != "completed") {
			return nil
		}
		candidates = response.Output
	}
	lastCompaction := -1
	for i, raw := range candidates {
		var header struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if json.Unmarshal(raw, &header) == nil && header.Type == "compaction" && header.EncryptedContent != "" {
			lastCompaction = i
		}
	}
	if lastCompaction < 0 {
		return nil
	}
	out := make(ResponsesWindow, 0, len(candidates)-lastCompaction)
	for _, raw := range candidates[lastCompaction:] {
		var item ResponsesItem
		if json.Unmarshal(raw, &item) != nil {
			return nil
		}
		out = append(out, item)
	}
	return out
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

func responseTextBlock(acc *Message, itemID string, outputIndex int) *Content {
	for i := range acc.Content {
		if acc.Content[i].Type != "text" {
			continue
		}
		if itemID != "" && acc.Content[i].ItemID == itemID {
			if outputIndex >= 0 {
				acc.Content[i].StreamIndex = outputIndex
			}
			bindResponseTextID(&acc.Content[i], itemID)
			return &acc.Content[i]
		}
	}
	for i := range acc.Content {
		if acc.Content[i].Type != "text" || outputIndex < 0 || acc.Content[i].StreamIndex != outputIndex {
			continue
		}
		if itemID != "" {
			acc.Content[i].ItemID = itemID
			bindResponseTextID(&acc.Content[i], itemID)
		}
		return &acc.Content[i]
	}
	if itemID != "" {
		var unbound *Content
		for i := range acc.Content {
			if acc.Content[i].Type != "text" || acc.Content[i].ItemID != "" {
				continue
			}
			if unbound != nil {
				unbound = nil
				break
			}
			unbound = &acc.Content[i]
		}
		if unbound != nil {
			unbound.ItemID = itemID
			if outputIndex >= 0 {
				unbound.StreamIndex = outputIndex
			}
			bindResponseTextID(unbound, itemID)
			return unbound
		}
	}
	if itemID == "" && outputIndex >= 0 {
		for i := range acc.Content {
			if acc.Content[i].Type == "text" && acc.Content[i].ItemID == "" && acc.Content[i].StreamIndex < 0 {
				acc.Content[i].StreamIndex = outputIndex
				return &acc.Content[i]
			}
		}
	}
	if itemID == "" && outputIndex < 0 {
		for i := range acc.Content {
			if acc.Content[i].Type == "text" && acc.Content[i].ItemID == "" {
				return &acc.Content[i]
			}
		}
	}
	block := Content{Type: "text", ItemID: itemID, StreamIndex: outputIndex}
	bindResponseTextID(&block, itemID)
	acc.Content = append(acc.Content, block)
	return &acc.Content[len(acc.Content)-1]
}

func bindResponseTextID(block *Content, itemID string) {
	if block == nil || itemID == "" {
		return
	}
	meta := responseOpaqueItem(block.TextSignature, "message")
	if meta == nil {
		meta = map[string]any{"v": 1, "type": "message"}
	}
	meta["id"] = itemID
	block.TextSignature = marshalArguments(meta)
}

func appendResponseText(acc *Message, itemID string, outputIndex int, delta string) {
	if delta == "" {
		return
	}
	responseTextBlock(acc, itemID, outputIndex).Text += delta
}

func mergeResponseText(acc *Message, itemID string, outputIndex int, text string) string {
	if text == "" {
		return ""
	}
	block := responseTextBlock(acc, itemID, outputIndex)
	if block.Text == text {
		return ""
	}
	if block.Text == "" {
		block.Text = text
		return text
	}
	if delta, ok := strings.CutPrefix(text, block.Text); ok {
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
		return errResponsesFunctionCallMissing
	}
	if raw != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return fmt.Errorf("invalid Responses function call arguments: %w", err)
		}
		if args == nil {
			return errResponsesFunctionCallArgsMustObject
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
