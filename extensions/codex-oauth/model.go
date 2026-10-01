package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type object = map[string]any

func obj(v any) object {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return object{}
}
func list(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return []any{}
}
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func integer(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		f, _ := n.Float64()
		return int64(f)
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case string:
		parsed := json.Number(n)
		f, _ := parsed.Float64()
		return int64(f)
	}
	return 0
}
func truth(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return integer(v) != 0
}
func fallback(v any, d any) any {
	if truth(v) {
		return v
	}
	return d
}
func get(m object, k string, d any) any {
	if v, ok := m[k]; ok {
		return v
	}
	return d
}
func clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := object{}
		for k, v := range x {
			m[k] = clone(v)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = clone(v)
		}
		return a
	}
	return v
}
func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }
func decode(s string, v any) error {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return errors.New("invalid trailing JSON data")
	}
	return nil
}
func shortHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:])[:32] }
func textOf(message object) string {
	var b strings.Builder
	for _, v := range list(message["content"]) {
		i := obj(v)
		if t := str(get(i, "type", "text")); t == "" || t == "text" {
			b.WriteString(str(i["text"]))
		}
	}
	return b.String()
}
func toolCalls(m object) []any {
	a := []any{}
	for _, v := range list(m["content"]) {
		if str(obj(v)["type"]) == "toolCall" {
			a = append(a, v)
		}
	}
	return a
}
func replayable(messages []any) []any {
	out := []any{}
	pending := []string{}
	remove := func(id string) {
		for i, v := range pending {
			if v == id {
				pending = append(pending[:i], pending[i+1:]...)
				break
			}
		}
	}
	for index, v := range messages {
		m := obj(v)
		role := str(m["role"])
		stop := str(m["stopReason"])
		if role == "assistant" && (stop == "aborted" || stop == "error" || textOf(m) == "" && len(list(m["content"])) == 0 && len(toolCalls(m)) == 0) {
			continue
		}
		if role == "toolResult" && len(out) == 0 {
			continue
		}
		current := m
		if role == "assistant" && len(toolCalls(m)) > 0 {
			current = clone(m).(object)
			for ci, v := range toolCalls(current) {
				call := obj(v)
				id := str(call["id"])
				if id == "" {
					// Older transcripts persisted empty call IDs. Recover the same
					// deterministic identity for the call and its following result.
					id = "call_pi_" + shortHash(fmt.Sprintf("%d:%d:%s", index, ci, str(call["itemId"])))
					call["id"] = id
				}
				pending = append(pending, id)
			}
		} else if role == "toolResult" {
			id := str(m["toolCallId"])
			if id != "" {
				remove(id)
			} else if len(pending) > 0 {
				current = clone(m).(object)
				current["toolCallId"] = pending[0]
				pending = pending[1:]
			}
		}
		out = append(out, current)
		if role == "assistant" {
			calls := toolCalls(current)
			nextResult := index+1 < len(messages) && str(obj(messages[index+1])["role"]) == "toolResult"
			if len(calls) > 0 && !nextResult {
				for _, v := range calls {
					call := obj(v)
					id := str(call["id"])
					out = append(out, object{"role": "toolResult", "toolCallId": id, "toolType": get(call, "toolType", "function"), "content": []any{object{"type": "text", "text": "No result provided"}}, "isError": true})
					remove(id)
				}
			}
		}
	}
	return out
}
func imageItem(i object) object {
	data := str(i["data"])
	if !strings.HasPrefix(data, "http://") && !strings.HasPrefix(data, "https://") && !strings.HasPrefix(data, "data:") {
		data = "data:" + str(get(i, "mimeType", "image/png")) + ";base64," + data
	}
	return object{"type": "input_image", "image_url": data, "detail": "auto"}
}
func responseItemID(v any, fallbackID string) string {
	id := str(fallback(v, fallbackID))
	if len([]rune(id)) <= 64 {
		return id
	}
	return "msg_pi_" + shortHash(id)
}
func reasoningKey(i object) string {
	if truth(i["thinkingSignature"]) {
		var signature object
		if decode(str(i["thinkingSignature"]), &signature) == nil && truth(signature["id"]) {
			return "id:" + str(signature["id"])
		}
	}
	if truth(i["itemId"]) {
		return "item:" + str(i["itemId"])
	}
	return ""
}

// Filter signatures because gateways that reuse output indexes can attach tool
// fields to reasoning items; replaying those fields poisons every later turn.
func reasoningPayload(i object) object {
	m := object{}
	for _, k := range []string{"type", "id", "summary", "content", "encrypted_content", "status", "phase"} {
		if v, ok := i[k]; ok {
			m[k] = v
		}
	}
	if _, ok := m["type"]; !ok {
		m["type"] = "reasoning"
	}
	return m
}
func inputItems(m, model object, index int) ([]any, error) {
	allowImage := false
	for _, v := range list(model["input"]) {
		if str(v) == "image" {
			allowImage = true
		}
	}
	role := str(m["role"])
	if role == "toolResult" {
		id := str(m["toolCallId"])
		if id == "" {
			return nil, errors.New("Codex tool result has no toolCallId")
		}
		out := []any{}
		hasImage := false
		for _, v := range list(m["content"]) {
			i := obj(v)
			switch str(i["type"]) {
			case "text", "":
				if text := str(i["text"]); text != "" {
					out = append(out, object{"type": "input_text", "text": text})
				}
			case "image":
				hasImage = true
				if allowImage && truth(i["data"]) {
					out = append(out, imageItem(i))
				}
			}
		}
		var output any = out
		if len(out) == 0 {
			output = "(no tool output)"
			if hasImage {
				output = "(see attached image)"
			}
		} else if len(out) == 1 && str(obj(out[0])["type"]) == "input_text" {
			output = obj(out[0])["text"]
		}
		kind := "function_call_output"
		if m["toolType"] == "custom" {
			kind = "custom_tool_call_output"
		}
		return []any{object{"type": kind, "call_id": id, "output": output}}, nil
	}
	if role == "assistant" {
		out := []any{}
		seen := map[string]bool{}
		for _, v := range list(m["content"]) {
			i := obj(v)
			if i["type"] == "thinking" && truth(i["thinkingSignature"]) {
				key := reasoningKey(i)
				if key != "" && seen[key] {
					continue
				}
				if key != "" {
					seen[key] = true
				}
				var signature object
				if decode(str(i["thinkingSignature"]), &signature) == nil && signature != nil {
					out = append(out, reasoningPayload(signature))
				}
			}
		}
		if text := textOf(m); text != "" {
			item := object{"type": "message", "role": "assistant", "status": "completed", "content": []any{object{"type": "output_text", "text": text, "annotations": []any{}}}}
			signature := ""
			for _, v := range list(m["content"]) {
				i := obj(v)
				if i["type"] == "text" && truth(i["textSignature"]) {
					signature = str(i["textSignature"])
					break
				}
			}
			if signature != "" {
				var meta object
				if decode(signature, &meta) == nil && meta != nil {
					for _, k := range []string{"id", "phase"} {
						if v, ok := meta[k]; ok {
							item[k] = v
						}
					}
				} else {
					item["id"] = responseItemID(signature, fmt.Sprintf("msg_pi_%d", index))
				}
			}
			item["id"] = responseItemID(item["id"], fmt.Sprintf("msg_pi_%d", index))
			out = append(out, item)
		}
		for _, v := range toolCalls(m) {
			call := obj(v)
			id := str(call["id"])
			if id == "" {
				return nil, errors.New("Codex assistant tool call has no id")
			}
			item := object{"call_id": id, "name": get(call, "name", "")}
			if call["toolType"] == "custom" {
				item["type"] = "custom_tool_call"
				item["input"] = get(call, "input", "")
			} else {
				item["type"] = "function_call"
				item["arguments"] = fallback(call["argumentsRaw"], jsonString(get(call, "arguments", object{})))
			}
			if truth(call["itemId"]) {
				item["id"] = call["itemId"]
			}
			out = append(out, item)
		}
		return out, nil
	}
	content := []any{}
	for _, v := range list(m["content"]) {
		i := obj(v)
		switch str(i["type"]) {
		case "text", "":
			if text := str(i["text"]); text != "" {
				content = append(content, object{"type": "input_text", "text": text})
			}
		case "image":
			if allowImage && truth(i["data"]) {
				content = append(content, imageItem(i))
			}
		}
	}
	if len(content) == 0 {
		return []any{}, nil
	}
	return []any{object{"type": "message", "role": "user", "content": content}}, nil
}
func buildRequest(payload object) (object, error) {
	request, model := obj(payload["request"]), obj(payload["model"])
	context := fallback(request["responsesContext"], []any{})
	ctx, ok := context.([]any)
	if !ok {
		return nil, errors.New("Codex Responses context must be an array of objects")
	}
	input := []any{}
	for _, v := range ctx {
		if _, ok := v.(map[string]any); !ok {
			return nil, errors.New("Codex Responses context must be an array of objects")
		}
		input = append(input, clone(v))
	}
	for index, v := range replayable(list(request["messages"])) {
		items, err := inputItems(obj(v), model, index)
		if err != nil {
			return nil, err
		}
		input = append(input, items...)
	}
	if len(input) == 0 {
		return nil, errors.New("Codex request has no input messages")
	}
	body := object{"model": get(model, "id", ""), "store": false, "stream": true, "instructions": fallback(request["system"], "You are a helpful assistant."), "input": input, "text": object{"verbosity": "low"}, "include": []any{"reasoning.encrypted_content"}, "tool_choice": "auto", "parallel_tool_calls": true}
	if session := str(request["sessionId"]); session != "" {
		r := []rune(session)
		if len(r) > 64 {
			r = r[:64]
		}
		body["prompt_cache_key"] = string(r)
	}
	if effort := str(request["thinkingEffort"]); effort != "" && effort != "off" {
		mapped := get(obj(request["thinkingLevelMap"]), effort, effort)
		if truth(mapped) {
			body["reasoning"] = object{"effort": mapped, "summary": "auto"}
		}
	}
	if tools := list(request["tools"]); len(tools) > 0 {
		entries := []any{}
		for _, v := range tools {
			tool := obj(v)
			entry := object{"name": get(tool, "name", ""), "description": get(tool, "description", "")}
			if tool["type"] == "custom" {
				entry["type"] = "custom"
				if tool["format"] != nil {
					entry["format"] = tool["format"]
				}
			} else {
				entry["type"] = "function"
				entry["parameters"] = get(tool, "parameters", object{})
			}
			entries = append(entries, entry)
		}
		body["tools"] = entries
	}
	return body, nil
}
func buildCompactRequest(payload object) (object, error) {
	body, err := buildRequest(payload)
	if err == nil {
		body["input"] = append(list(body["input"]), object{"type": "compaction_trigger"})
	}
	return body, err
}
func retainedCompactionItems(input []any) []any {
	remaining := 64000
	retained := []any{}
	for i := len(input) - 1; i >= 0; i-- {
		item := obj(input[i])
		if item["type"] != "message" || item["role"] != "user" {
			continue
		}
		tokens := (pythonJSONLength(item) + 3) / 4
		if tokens < 1 {
			tokens = 1
		}
		if tokens > remaining {
			break
		}
		remaining -= tokens
		retained = append(retained, clone(item))
	}
	for i, j := 0, len(retained)-1; i < j; i, j = i+1, j-1 {
		retained[i], retained[j] = retained[j], retained[i]
	}
	return retained
}

// Python estimated retention using ASCII-escaped JSON. Preserve that budget for
// non-ASCII text as well, so the migration cannot retain a larger canonical window.
func pythonJSONLength(v any) int {
	s := jsonString(v)
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, `\u003c`, "<"), `\u003e`, ">"), `\u0026`, "&")
	n := 0
	for _, r := range s {
		if r > 127 {
			if r > 0xffff {
				n += 12
			} else {
				n += 6
			}
		} else {
			n++
		}
	}
	return n
}
func responseUsage(v any) object {
	u, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	d := obj(u["input_tokens_details"])
	cached := integer(d["cached_tokens"])
	write := integer(get(d, "cache_write_tokens", u["cache_write_tokens"]))
	input := integer(u["input_tokens"]) - cached - write
	if input < 0 {
		input = 0
	}
	return object{"input": input, "output": integer(u["output_tokens"]), "cacheRead": cached, "cacheWrite": write, "totalTokens": integer(u["total_tokens"])}
}
