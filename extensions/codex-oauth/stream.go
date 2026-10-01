package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type slotRegistry struct{ byIndex, byCall map[string]string }

func newSlots() *slotRegistry { return &slotRegistry{map[string]string{}, map[string]string{}} }

// Gateway output indexes may be reused or absent. Provider item IDs keep
// reasoning and tool calls separate; ambiguous deltas reach the latest item.
func (s *slotRegistry) resolve(o object, itemID, callID string) string {
	index := ""
	if o["output_index"] != nil {
		index = str(o["output_index"])
	}
	if itemID != "" {
		slot := "id:" + itemID
		if index != "" {
			s.byIndex[index] = slot
		}
		if callID != "" {
			s.byCall[callID] = slot
		}
		return slot
	}
	if callID != "" && s.byCall[callID] != "" {
		return s.byCall[callID]
	}
	if index != "" && s.byIndex[index] != "" {
		return s.byIndex[index]
	}
	if callID != "" {
		return "call:" + callID
	}
	if index != "" {
		return "index:" + index
	}
	return "item:unknown"
}
func emitEvent(send func(object), kind string, message object, values object) {
	p := object{"requestId": get(values, "requestId", ""), "type": kind, "message": clone(message)}
	for k, v := range values {
		if v != nil && v != "" {
			p[k] = v
		}
	}
	send(object{"jsonrpc": "2.0", "method": "provider.stream.event", "params": p})
}
func responseID(o object) string {
	r, ok := o["response"].(map[string]any)
	if !ok {
		r = o
	}
	return str(r["id"])
}

type streamBuilder struct {
	send                                                               func(object)
	requestID                                                          string
	message                                                            object
	items                                                              map[string]object
	slots                                                              *slotRegistry
	startedText, endedText, startedThinking, endedThinking, endedTools map[string]bool
	indices                                                            map[string]int
	terminal                                                           bool
	eventName                                                          string
	dataLines                                                          []string
}

func newStreamBuilder(send func(object), id string, model object) *streamBuilder {
	b := &streamBuilder{send: send, requestID: id, message: object{"role": "assistant", "api": get(model, "api", "openai-codex-responses"), "provider": get(model, "provider", "openai-codex"), "model": get(model, "id", ""), "content": []any{}}, items: map[string]object{}, slots: newSlots(), startedText: map[string]bool{}, endedText: map[string]bool{}, startedThinking: map[string]bool{}, endedThinking: map[string]bool{}, endedTools: map[string]bool{}, indices: map[string]int{}}
	emitEvent(send, "start", b.message, object{"requestId": id})
	return b
}
func (b *streamBuilder) emit(kind, key string, values object) {
	if values == nil {
		values = object{}
	}
	values["requestId"] = b.requestID
	if key != "" {
		i, ok := b.indices[key]
		if !ok {
			i = len(b.indices)
			b.indices[key] = i
		}
		values["contentIndex"] = i
	}
	emitEvent(b.send, kind, b.message, values)
}
func (b *streamBuilder) item(key string, initial object) object {
	if i := b.items[key]; i != nil {
		return i
	}
	b.items[key] = initial
	return initial
}
func (b *streamBuilder) append(i object) {
	b.message["content"] = append(list(b.message["content"]), i)
}
func (b *streamBuilder) contains(i object) bool {
	for _, v := range list(b.message["content"]) {
		if reflect.DeepEqual(obj(v), i) {
			return true
		}
	}
	return false
}
func (b *streamBuilder) feed(line string) (bool, error) {
	if line == "" {
		if len(b.dataLines) > 0 {
			data := strings.Join(b.dataLines, "\n")
			if data != "[DONE]" {
				var o object
				if decode(data, &o) != nil || o == nil {
					return false, errors.New("Codex stream contained invalid JSON")
				}
				if err := b.process(b.eventName, o); err != nil {
					return false, err
				}
			}
		}
		b.eventName = ""
		b.dataLines = nil
		return b.terminal, nil
	}
	if strings.HasPrefix(line, "event:") {
		b.eventName = strings.TrimSpace(line[6:])
	} else if strings.HasPrefix(line, "data:") {
		b.dataLines = append(b.dataLines, strings.TrimLeft(line[5:], " \t"))
	}
	return false, nil
}
func (b *streamBuilder) process(name string, o object) error {
	typ := str(fallback(o["type"], name))
	if typ == "response.created" || typ == "response.in_progress" || typ == "response.queued" {
		b.message["responseId"] = fallback(responseID(o), get(b.message, "responseId", ""))
		return nil
	}
	item := obj(o["item"])
	callID := str(fallback(o["call_id"], item["call_id"]))
	itemID := str(fallback(o["item_id"], fallback(item["id"], callID)))
	slot := b.slots.resolve(o, str(fallback(o["item_id"], item["id"])), callID)
	if (typ == "response.output_item.added" || typ == "response.output_item.done") && len(item) > 0 {
		switch str(item["type"]) {
		case "message":
			i := b.item(slot, object{"type": "text", "text": "", "itemId": itemID})
			if truth(item["id"]) {
				i["itemId"] = item["id"]
				sig := object{"v": 1, "id": item["id"]}
				if truth(item["phase"]) {
					sig["phase"] = item["phase"]
				}
				i["textSignature"] = jsonString(sig)
			}
			for _, v := range list(item["content"]) {
				p := obj(v)
				if p["type"] == "output_text" && truth(p["text"]) {
					i["text"] = p["text"]
				}
			}
			if typ == "response.output_item.done" {
				if truth(i["text"]) && !b.startedText[slot] {
					b.startedText[slot] = true
					b.append(i)
					b.emit("text_start", slot, nil)
					b.emit("text_delta", slot, object{"delta": i["text"]})
				}
				if b.startedText[slot] && !b.endedText[slot] {
					b.endedText[slot] = true
					b.emit("text_end", slot, nil)
				}
			}
		case "function_call", "custom_tool_call":
			if itemID == "" {
				return errors.New("Codex response tool call has no item_id")
			}
			callID = str(item["call_id"])
			if callID == "" {
				return errors.New("Codex response tool call has no call_id")
			}
			toolType := "function"
			if item["type"] == "custom_tool_call" {
				toolType = "custom"
			}
			i := b.item(slot, object{"type": "toolCall", "id": callID, "itemId": get(item, "id", itemID), "name": get(item, "name", ""), "arguments": object{}, "argumentsRaw": "", "toolType": toolType, "input": get(item, "input", "")})
			i["id"] = callID
			i["name"] = get(item, "name", get(i, "name", ""))
			i["itemId"] = get(item, "id", get(i, "itemId", ""))
			if item["type"] == "function_call" && item["arguments"] != nil {
				i["argumentsRaw"] = str(item["arguments"])
				if typ == "response.output_item.done" && truth(i["argumentsRaw"]) {
					parsed, err := parseArguments(str(i["argumentsRaw"]))
					if err != nil {
						return err
					}
					i["arguments"] = parsed
				}
			}
			if item["type"] == "custom_tool_call" && item["input"] != nil {
				i["input"] = str(item["input"])
			}
			if typ == "response.output_item.done" {
				if !b.contains(i) {
					b.append(i)
					b.emit("toolcall_start", slot, object{"toolCallId": i["id"], "toolName": i["name"]})
				}
				if !b.endedTools[slot] {
					b.endedTools[slot] = true
					b.emit("toolcall_end", slot, object{"toolCallId": i["id"], "toolName": i["name"]})
				}
			}
		case "reasoning":
			i := b.item(slot, object{"type": "thinking", "thinking": "", "itemId": itemID})
			if truth(item["encrypted_content"]) {
				i["thinkingSignature"] = jsonString(reasoningPayload(item))
			}
			summary := summaryText(list(item["summary"]))
			if summary == "" {
				summary = summaryText(list(item["content"]))
			}
			if summary != "" {
				i["thinking"] = summary
			}
			if !b.contains(i) {
				b.append(i)
			}
			if typ == "response.output_item.done" {
				if !b.startedThinking[slot] {
					b.startedThinking[slot] = true
					b.emit("thinking_start", slot, nil)
					if truth(i["thinking"]) {
						b.emit("thinking_delta", slot, object{"delta": i["thinking"]})
					}
				}
				if !b.endedThinking[slot] {
					b.endedThinking[slot] = true
					b.emit("thinking_end", slot, nil)
				}
			}
		}
		return nil
	}
	switch typ {
	case "response.content_part.added", "response.content_part.done":
		if itemID == "" {
			return errors.New("Codex content event has no item_id")
		}
		i := b.item(slot, object{"type": "text", "text": "", "itemId": itemID})
		part := obj(o["part"])
		text := ""
		if part["type"] == "output_text" || part["type"] == "refusal" {
			text = str(get(part, "text", get(part, "refusal", "")))
		}
		old := str(i["text"])
		delta := text
		if old == text {
			delta = ""
		} else if old != "" && strings.HasPrefix(text, old) {
			delta = text[len(old):]
		} else if old != "" {
			delta = ""
		}
		if text != "" {
			i["text"] = text
		}
		if delta != "" {
			if !b.startedText[slot] {
				b.startedText[slot] = true
				b.append(i)
				b.emit("text_start", slot, nil)
			}
			b.emit("text_delta", slot, object{"delta": delta})
		}
	case "response.output_text.delta", "response.refusal.delta":
		if itemID == "" {
			return errors.New("Codex text event has no item_id")
		}
		i := b.item(slot, object{"type": "text", "text": "", "itemId": itemID})
		if !b.startedText[slot] {
			b.startedText[slot] = true
			b.append(i)
			b.emit("text_start", slot, nil)
		}
		delta := str(get(o, "delta", ""))
		i["text"] = str(i["text"]) + delta
		b.emit("text_delta", slot, object{"delta": delta})
	case "response.output_text.done":
		if itemID == "" {
			return errors.New("Codex text event has no item_id")
		}
		i := b.item(slot, object{"type": "text", "text": "", "itemId": itemID})
		if o["text"] != nil {
			i["text"] = str(o["text"])
		}
		if !b.startedText[slot] && truth(i["text"]) {
			b.startedText[slot] = true
			b.append(i)
			b.emit("text_start", slot, nil)
			b.emit("text_delta", slot, object{"delta": i["text"]})
		}
		if b.startedText[slot] && !b.endedText[slot] {
			b.endedText[slot] = true
			b.emit("text_end", slot, nil)
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if itemID == "" {
			return errors.New("Codex reasoning event has no item_id")
		}
		i := b.item(slot, object{"type": "thinking", "thinking": "", "itemId": itemID})
		delta := str(get(o, "delta", ""))
		if typ == "response.reasoning_summary_part.added" {
			delta = str(get(obj(o["part"]), "text", ""))
		}
		if !b.startedThinking[slot] {
			b.startedThinking[slot] = true
			b.append(i)
			b.emit("thinking_start", slot, nil)
		}
		i["thinking"] = str(i["thinking"]) + delta
		b.emit("thinking_delta", slot, object{"delta": delta})
	case "response.reasoning_summary_part.done":
		if b.startedThinking[slot] {
			b.emit("thinking_delta", slot, object{"delta": "\n\n"})
		}
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		if b.startedThinking[slot] && !b.endedThinking[slot] {
			b.endedThinking[slot] = true
			b.emit("thinking_end", slot, nil)
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		custom := strings.HasPrefix(typ, "response.custom")
		callID = str(o["call_id"])
		if itemID == "" {
			return errors.New("Codex tool call event has no item_id")
		}
		i := b.items[slot]
		if i == nil {
			if callID == "" {
				return errors.New("Codex tool call delta has no prior output item")
			}
			i = b.item(slot, newTool(o, callID, itemID, custom))
		}
		if callID != "" {
			i["id"] = callID
		}
		if !b.contains(i) {
			b.append(i)
			b.emit("toolcall_start", slot, object{"toolCallId": i["id"], "toolName": i["name"]})
		}
		delta := str(get(o, "delta", get(o, "input", "")))
		kind := "toolcall_delta"
		field := "argumentsRaw"
		if custom {
			kind = "custom_tool_call_input_delta"
			field = "input"
		}
		i[field] = str(i[field]) + delta
		b.emit(kind, slot, object{"delta": delta, "toolCallId": i["id"], "toolName": i["name"]})
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		i := b.items[slot]
		callID = str(o["call_id"])
		if i == nil && callID != "" {
			i = b.item(slot, newTool(o, callID, str(fallback(itemID, callID)), strings.HasPrefix(typ, "response.custom")))
		}
		if i == nil {
			return errors.New("Codex tool call completion has no prior output item")
		}
		if !truth(i["id"]) {
			if callID == "" {
				return errors.New("Codex tool call event has no call_id")
			}
			i["id"] = callID
		}
		if !b.contains(i) {
			b.append(i)
			b.emit("toolcall_start", slot, object{"toolCallId": i["id"], "toolName": i["name"]})
		}
		if o["arguments"] != nil {
			i["argumentsRaw"] = str(o["arguments"])
			parsed, err := parseArguments(str(i["argumentsRaw"]))
			if err != nil {
				return err
			}
			i["arguments"] = parsed
		}
		if o["input"] != nil {
			i["input"] = str(o["input"])
		}
		if !b.endedTools[slot] {
			b.endedTools[slot] = true
			b.emit("toolcall_end", slot, object{"toolCallId": i["id"], "toolName": i["name"]})
		}
	case "response.done", "response.completed", "response.incomplete", "response.failed", "response.cancelled", "error":
		response, ok := o["response"].(map[string]any)
		if !ok {
			response = o
		}
		// Terminal output is authoritative when intermediate item events were
		// omitted; replay through the normal path to retain IDs and signatures.
		for index, v := range list(response["output"]) {
			if item, ok := v.(map[string]any); ok {
				if err := b.process("response.output_item.done", object{"type": "response.output_item.done", "output_index": index, "item": item}); err != nil {
					return err
				}
			}
		}
		if id := responseID(o); id != "" {
			b.message["responseId"] = id
		}
		if usage := responseUsage(response["usage"]); usage != nil {
			b.message["usage"] = usage
		}
		status := str(response["status"])
		if typ == "response.failed" || typ == "error" || status == "failed" {
			b.message["stopReason"] = "error"
			b.message["errorMessage"] = providerError(o, response, "Codex response failed")
		} else if typ == "response.cancelled" || status == "cancelled" {
			b.message["stopReason"] = "aborted"
		} else if typ == "response.incomplete" || status == "incomplete" {
			reason := str(obj(response["incomplete_details"])["reason"])
			if reason == "max_output_tokens" {
				b.message["stopReason"] = "length"
			} else {
				b.message["stopReason"] = "error"
				detail := "Response incomplete without a provider reason"
				if reason != "" {
					detail = "Response incomplete: " + reason
				}
				b.message["errorMessage"] = detail
			}
		} else {
			reason := "stop"
			if len(toolCalls(b.message)) > 0 {
				reason = "toolUse"
			}
			b.message["stopReason"] = reason
		}
		b.terminal = true
	}
	return nil
}
func newTool(o object, id, itemID string, custom bool) object {
	t := "function"
	if custom {
		t = "custom"
	}
	return object{"type": "toolCall", "id": id, "itemId": itemID, "name": get(o, "name", ""), "toolType": t, "argumentsRaw": "", "arguments": object{}, "input": ""}
}
func parseArguments(raw string) (object, error) {
	var v any
	if decode(raw, &v) != nil {
		return nil, errors.New("Codex function call arguments are not valid JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("Codex function call arguments must be a JSON object")
	}
	return m, nil
}
func summaryText(a []any) string {
	parts := []string{}
	for _, v := range a {
		if text := str(obj(v)["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}
func providerError(o, response object, fallbackError string) string {
	e := response["error"]
	if m, ok := e.(map[string]any); ok {
		e = m["message"]
	}
	return str(fallback(o["message"], fallback(e, fallbackError)))
}

type compactBuilder struct {
	output     []any
	usage      object
	terminal   bool
	eventName  string
	dataLines  []string
	eventBytes int
}

func newCompactBuilder() *compactBuilder { return &compactBuilder{output: []any{}} }
func (b *compactBuilder) feed(line string) (bool, error) {
	if line == "" {
		if len(b.dataLines) > 0 {
			data := strings.Join(b.dataLines, "\n")
			if data != "[DONE]" {
				var v any
				if decode(data, &v) != nil {
					return false, errors.New("Codex compaction stream contained invalid JSON")
				}
				o, ok := v.(map[string]any)
				if !ok {
					return false, errors.New("Codex compaction stream event is not an object")
				}
				if err := b.process(b.eventName, o); err != nil {
					return false, err
				}
			}
		}
		b.eventName = ""
		b.dataLines = nil
		b.eventBytes = 0
		return b.terminal, nil
	}
	b.eventBytes += len(line)
	if b.eventBytes > 64*1024*1024 {
		return false, errors.New("Codex compaction stream event is too large")
	}
	if strings.HasPrefix(line, "event:") {
		b.eventName = strings.TrimSpace(line[6:])
	} else if strings.HasPrefix(line, "data:") {
		b.dataLines = append(b.dataLines, strings.TrimLeft(line[5:], " \t"))
	}
	return false, nil
}
func (b *compactBuilder) process(name string, o object) error {
	typ := str(fallback(o["type"], name))
	if typ == "response.output_item.done" {
		if i, ok := o["item"].(map[string]any); ok {
			b.output = append(b.output, clone(i))
		}
		return nil
	}
	switch typ {
	case "response.done", "response.completed", "response.incomplete", "response.failed", "response.cancelled", "error":
	default:
		return nil
	}
	r, ok := o["response"].(map[string]any)
	if !ok {
		r = o
	}
	if v, exists := r["output"]; exists {
		a, ok := v.([]any)
		if !ok {
			return errors.New("Codex compaction terminal output is invalid")
		}
		for _, v := range a {
			if _, ok := v.(map[string]any); !ok {
				return errors.New("Codex compaction terminal output is invalid")
			}
		} // V2 delivers the checkpoint incrementally and often completes with output: [].
		if len(a) > 0 && len(b.output) == 0 {
			b.output = clone(a).([]any)
		}
	}
	b.usage = responseUsage(r["usage"])
	status := str(r["status"])
	if typ == "response.failed" || typ == "error" || status == "failed" {
		return errors.New(providerError(o, r, "Codex compaction failed"))
	}
	if typ == "response.cancelled" || status == "cancelled" {
		return errors.New("Codex compaction was cancelled")
	}
	if typ == "response.incomplete" || status == "incomplete" {
		reason := str(obj(r["incomplete_details"])["reason"])
		if reason != "" {
			reason = ": " + reason
		}
		return errors.New("Codex compaction response was incomplete" + reason)
	}
	if (typ != "response.done" && typ != "response.completed") || (status != "" && status != "completed") {
		return fmt.Errorf("Codex compaction ended with unexpected status %s", fallback(status, typ))
	}
	b.terminal = true
	return nil
}
func (b *compactBuilder) result() (object, error) {
	count := 0
	for _, v := range b.output {
		if obj(v)["type"] == "compaction" {
			count++
		}
	}
	if count != 1 {
		return nil, fmt.Errorf("Codex Remote Compaction V2 expected exactly one compaction output item, got %d from %d output items", count, len(b.output))
	}
	r := object{"items": clone(b.output)}
	if b.usage != nil {
		r["usage"] = b.usage
	}
	return r, nil
}
