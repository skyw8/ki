package main

import (
	"encoding/json"
	"strings"
)

func kiRequestToChat(request object) raceInput {
	input := raceInput{Messages: toOpenRouterMessages(request), MaxTokens: uintValue(request["maxTokens"])}
	for _, v := range arr(request["tools"]) {
		t := obj(v)
		input.Tools = append(input.Tools, object{"type": "function", "function": object{"name": str(t["name"]), "description": or(t["description"], ""), "parameters": or(t["parameters"], object{})}})
	}
	return input
}
func toOpenRouterMessages(request object) []any {
	out := []any{}
	if system := str(request["system"]); system != "" {
		out = append(out, object{"role": "system", "content": system})
	}
	for _, v := range arr(request["messages"]) {
		m := obj(v)
		role := str(m["role"])
		if role == "toolResult" {
			text := messageText(m)
			if text == "" {
				for _, c := range arr(m["content"]) {
					if str(obj(c)["type"]) == "image" {
						text = "(see attached image)"
						break
					}
				}
			}
			out = append(out, object{"role": "tool", "tool_call_id": str(m["toolCallId"]), "content": text})
			continue
		}
		if role == "assistant" {
			entry := object{"role": "assistant"}
			var text strings.Builder
			calls := []any{}
			for _, v := range arr(m["content"]) {
				c := obj(v)
				switch str(c["type"]) {
				case "text", "":
					text.WriteString(str(c["text"]))
				case "thinking":
					if t := str(c["thinking"]); t != "" {
						entry["reasoning_content"] = t
					}
				case "toolCall":
					args := validObjectArgumentsRaw(str(c["argumentsRaw"]))
					if args == "" {
						args = jsonText(or(c["arguments"], object{}))
					}
					calls = append(calls, object{"id": or(c["id"], ""), "type": "function", "function": object{"name": or(c["name"], ""), "arguments": args}})
				}
			}
			if text.Len() > 0 {
				entry["content"] = text.String()
			}
			if len(calls) > 0 {
				entry["tool_calls"] = calls
			}
			out = append(out, entry)
			continue
		}
		parts := []any{}
		hasMedia := false
		for _, v := range arr(m["content"]) {
			c := obj(v)
			typ := str(c["type"])
			if typ == "image" {
				if data, ok := c["data"].(string); ok {
					hasMedia = true
					mime, ok := c["mimeType"].(string)
					if !ok {
						mime = "image/png"
					}
					parts = append(parts, object{"type": "image_url", "image_url": object{"url": "data:" + mime + ";base64," + data}})
				}
			} else if typ == "text" || typ == "" {
				if t := str(c["text"]); t != "" {
					parts = append(parts, object{"type": "text", "text": t})
				}
			}
		}
		var content any = messageText(m)
		if hasMedia {
			content = parts
		}
		out = append(out, object{"role": "user", "content": content})
	}
	return out
}
func messageText(message object) string {
	var text strings.Builder
	for _, v := range arr(message["content"]) {
		c := obj(v)
		if typ := str(c["type"]); typ == "text" || typ == "" {
			text.WriteString(str(c["text"]))
		}
	}
	return text.String()
}
func validObjectArgumentsRaw(raw string) string {
	var parsed any
	if raw != "" && json.Unmarshal([]byte(raw), &parsed) == nil && obj(parsed) != nil {
		return raw
	}
	return ""
}
