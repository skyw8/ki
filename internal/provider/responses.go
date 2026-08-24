package provider

import (
	"context"
	"encoding/json"
	"strings"

	"ki/internal/loop"
	"ki/internal/types"
)

// ResponsesBody is the OpenAI Responses API payload.
func ResponsesBody(req loop.Request) map[string]any {
	var input []any
	for _, m := range replayable(req.Messages) {
		input = append(input, toResponsesItems(m)...)
	}
	body := map[string]any{
		"model":  req.Model,
		"input":  input,
		"stream": true,
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
			} else {
				tools = append(tools, map[string]any{
					"type":        "function",
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				})
			}
		}
		body["tools"] = tools
	}
	return body
}

func toResponsesItems(m types.Message) []any {
	switch m.Role {
	case "toolResult":
		// Responses allows function_call_output.output to be an input_text plus
		// input_image array (as pi and Codex do). Keep images in this output rather
		// than inserting another user message, and keep parallel outputs together.
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
		for _, c := range m.Content {
			switch c.Type {
			case "text", "":
				text.WriteString(c.Text)
			case "toolCall":
				item := map[string]any{"call_id": c.ID, "name": c.Name}
				if c.ToolType == "custom" {
					item["type"] = "custom_tool_call"
					item["input"] = c.Input
				} else {
					item["type"] = "function_call"
					item["arguments"] = marshalArguments(c.Arguments)
				}
				if c.ItemID != "" {
					item["id"] = c.ItemID
				}
				items = append(items, item)
			}
		}
		if text.String() != "" {
			items = append([]any{map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": text.String()}},
			}}, items...)
		}
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

func responsesToolOutput(m types.Message) any {
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

func responsesUserContent(m types.Message) []map[string]any {
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

func responsesImageParts(m types.Message) []map[string]any {
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

func (l *Live) streamResponses(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	url := l.Base + "/responses"
	return l.postStream(ctx, url, ResponsesBody(req), l.oaHeaders(), emit, parseResponsesSSE)
}

func parseResponsesSSE(event, data string, acc *types.Message) (loop.AssistantDelta, bool) {
	var obj map[string]any
	if json.Unmarshal([]byte(data), &obj) != nil {
		return loop.AssistantDelta{}, false
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		typ = event
	}
	switch typ {
	case "response.output_text.delta":
		delta, _ := obj["delta"].(string)
		appendText(acc, delta)
		return loop.AssistantDelta{Type: "text_delta", Delta: delta, Partial: *acc}, true
	case "response.output_item.added", "response.output_item.done":
		if item, ok := obj["item"].(map[string]any); ok {
			itemType, _ := item["type"].(string)
			switch itemType {
			case "function_call":
				itemID, _ := item["id"].(string)
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				args, _ := item["arguments"].(string)
				id := callID
				if id == "" {
					id = itemID
				}
				c := findOrAddToolCall(acc, id, itemID, name, -1)
				if args != "" {
					appendToolArgs(c, args)
				}
				if typ == "response.output_item.done" && len(acc.ToolCalls()) > 0 {
					acc.StopReason = "toolUse"
				}
			case "custom_tool_call":
				itemID, _ := item["id"].(string)
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				input, _ := item["input"].(string)
				id := callID
				if id == "" {
					id = itemID
				}
				c := findOrAddToolCall(acc, id, itemID, name, -1)
				c.ToolType = "custom"
				if input != "" {
					c.Input = input
				}
				if typ == "response.output_item.done" {
					acc.StopReason = "toolUse"
				}
			}
		}
	case "response.custom_tool_call_input.delta":
		itemID, _ := obj["item_id"].(string)
		callID, _ := obj["call_id"].(string)
		delta, _ := obj["delta"].(string)
		c := findOrAddToolCall(acc, callID, itemID, "", -1)
		c.ToolType = "custom"
		c.Input += delta
		return loop.AssistantDelta{Type: "custom_tool_call_input_delta", Delta: delta, ToolCallID: c.ID, ToolName: c.Name, Partial: *acc}, delta != ""
	case "response.custom_tool_call_input.done":
		itemID, _ := obj["item_id"].(string)
		callID, _ := obj["call_id"].(string)
		input, _ := obj["input"].(string)
		c := findOrAddToolCall(acc, callID, itemID, "", -1)
		c.ToolType = "custom"
		if input != "" {
			c.Input = input
		}
		acc.StopReason = "toolUse"
	case "response.function_call_arguments.delta":
		itemID, _ := obj["item_id"].(string)
		delta, _ := obj["delta"].(string)
		c := findOrAddToolCall(acc, "", itemID, "", -1)
		appendToolArgs(c, delta)
	case "response.function_call_arguments.done":
		itemID, _ := obj["item_id"].(string)
		args, _ := obj["arguments"].(string)
		c := findOrAddToolCall(acc, "", itemID, "", -1)
		if args != "" {
			c.ArgumentsRaw = args
			var dst map[string]any
			if json.Unmarshal([]byte(args), &dst) == nil {
				c.Arguments = dst
			}
		}
		if len(acc.ToolCalls()) > 0 {
			acc.StopReason = "toolUse"
		}
	case "response.completed":
		if len(acc.ToolCalls()) > 0 {
			acc.StopReason = "toolUse"
		} else if acc.StopReason == "" {
			acc.StopReason = "stop"
		}
		if resp, ok := obj["response"].(map[string]any); ok {
			if u, ok := resp["usage"].(map[string]any); ok {
				cached := asInt(u["cached_tokens"])
				if details, ok := u["input_tokens_details"].(map[string]any); ok {
					cached = asInt(details["cached_tokens"])
				}
				acc.Usage = &types.Usage{
					Input:       asInt(u["input_tokens"]),
					Output:      asInt(u["output_tokens"]),
					CacheRead:   cached,
					TotalTokens: asInt(u["total_tokens"]),
				}
			}
		}
	}
	return loop.AssistantDelta{}, false
}
