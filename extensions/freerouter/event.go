package main

import "encoding/json"

type object = map[string]any

func obj(v any) object { o, _ := v.(map[string]any); return o }
func arr(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func uintValue(v any) uint64 {
	switch n := v.(type) {
	case float64:
		if n >= 0 && n == float64(uint64(n)) {
			return uint64(n)
		}
	case uint64:
		return n
	case int:
		if n >= 0 {
			return uint64(n)
		}
	}
	return 0
}
func or(v, fallback any) any {
	if v == nil {
		return fallback
	}
	return v
}
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func cloneObject(v object) object {
	var copy object
	_ = json.Unmarshal([]byte(jsonText(v)), &copy)
	return copy
}
func event(typ string, fields object) object {
	if fields == nil {
		fields = object{}
	}
	fields["type"] = typ
	return fields
}
func streamError(reason, message string) object {
	return event("error", object{"reason": reason, "error": message})
}
func qualifies(ev object) bool {
	switch str(ev["type"]) {
	case "text_start", "toolcall_start":
		return true
	case "done":
		for _, v := range arr(obj(ev["message"])["content"]) {
			c := obj(v)
			if str(c["type"]) == "toolCall" || (str(c["type"]) == "text" && str(c["text"]) != "") {
				return true
			}
		}
	}
	return false
}
func remapIndex(ev object, offset int) object {
	if idx, ok := ev["contentIndex"]; ok {
		ev = cloneObject(ev)
		ev["contentIndex"] = uintValue(idx) + uint64(offset)
	}
	return ev
}
func prependThinking(message object, thinking string) object {
	message = cloneObject(message)
	message["content"] = append([]any{object{"type": "thinking", "thinking": thinking}}, arr(message["content"])...)
	return message
}
