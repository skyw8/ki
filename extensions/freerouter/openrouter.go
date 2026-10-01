package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type modelError struct {
	Kind    string
	ModelID string
	Status  int
	Message string
}

func (e *modelError) Error() string {
	if e.Kind == "exhausted" {
		return fmt.Sprintf("Model %s quota exceeded (HTTP %d)", e.ModelID, e.Status)
	}
	if e.Kind == "cancelled" {
		return "cancelled"
	}
	return e.Message
}
func buildChatBody(id string, input raceInput) object {
	body := object{"model": id, "stream": true, "messages": input.Messages}
	if input.MaxTokens > 0 {
		body["max_tokens"] = input.MaxTokens
	}
	if len(input.Tools) > 0 {
		body["tools"] = input.Tools
	}
	return body
}
func normalizeStopReason(reason string) string {
	switch reason {
	case "tool_calls":
		return "toolUse"
	case "length":
		return "length"
	}
	return "stop"
}
func nowMS() int64 { return time.Now().UnixMilli() }
func newAssistantMessage(id string) object {
	return object{"role": "assistant", "api": "freerouter", "provider": "free-router", "model": id, "content": []any{}, "usage": object{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0}, "stopReason": "stop", "timestamp": nowMS()}
}

type pendingTool struct {
	ContentIndex         int
	ID, Name, ArgsBuffer string
}
type streamParser struct {
	modelID   string
	output    object
	textIndex int
	pending   map[uint64]*pendingTool
	emit      func(object)
}

func newStreamParser(modelID string, emit func(object)) *streamParser {
	return &streamParser{modelID: modelID, output: newAssistantMessage(modelID), textIndex: -1, pending: map[uint64]*pendingTool{}, emit: emit}
}
func (p *streamParser) handle(chunk object) error {
	if errObj, ok := chunk["error"]; ok {
		e := obj(errObj)
		code := uintValue(e["code"])
		if code == 0 {
			code = uintValue(e["status"])
		}
		if code == 402 {
			msg, hasMessage := e["message"].(string)
			if !hasMessage {
				msg = "insufficient credits"
			}
			return &modelError{Kind: "fatal", Message: msg}
		}
		if code == 0 {
			code = 400
		}
		return &modelError{Kind: "exhausted", ModelID: p.modelID, Status: int(code)}
	}
	var choice object
	if choices := arr(chunk["choices"]); len(choices) > 0 {
		choice = obj(choices[0])
	}
	delta := obj(choice["delta"])
	if usage, ok := chunk["usage"]; ok {
		u := obj(usage)
		p.output["usage"] = object{"input": uintValue(u["prompt_tokens"]), "output": uintValue(u["completion_tokens"]), "cacheRead": 0, "cacheWrite": 0, "totalTokens": uintValue(u["total_tokens"])}
	}
	if finish, ok := choice["finish_reason"].(string); ok {
		p.output["stopReason"] = normalizeStopReason(finish)
	}
	// Empty or reasoning-only chunks cannot win: free models may stop without
	// producing any visible output after emitting these preliminary deltas.
	if text := str(delta["content"]); text != "" {
		if p.textIndex < 0 {
			// Tool calls may precede text. Track the actual block index so that text
			// never overwrites an already streamed tool call.
			p.textIndex = len(arr(p.output["content"]))
			p.output["content"] = append(arr(p.output["content"]), object{"type": "text", "text": ""})
			p.emit(event("text_start", object{"contentIndex": p.textIndex}))
		}
		block := obj(arr(p.output["content"])[p.textIndex])
		block["text"] = str(block["text"]) + text
		p.emit(event("text_delta", object{"contentIndex": p.textIndex, "delta": text}))
	}
	for _, v := range arr(delta["tool_calls"]) {
		tc := obj(v)
		idx := uintValue(tc["index"])
		fn := obj(tc["function"])
		if id, ok := tc["id"].(string); ok {
			contentIndex := len(arr(p.output["content"]))
			name := str(fn["name"])
			p.output["content"] = append(arr(p.output["content"]), object{"type": "toolCall", "id": id, "name": name, "arguments": object{}})
			p.pending[idx] = &pendingTool{ContentIndex: contentIndex, ID: id, Name: name, ArgsBuffer: str(fn["arguments"])}
			p.emit(event("toolcall_start", object{"contentIndex": contentIndex, "toolCallId": id, "toolName": name, "toolCall": object{"type": "toolCall", "id": id, "name": name}}))
		} else if args, ok := fn["arguments"].(string); ok {
			if pending := p.pending[idx]; pending != nil {
				pending.ArgsBuffer += args
				p.emit(event("toolcall_delta", object{"contentIndex": pending.ContentIndex, "delta": args, "toolCallId": pending.ID}))
			}
		}
	}
	return nil
}
func (p *streamParser) closeBlocks() {
	if p.textIndex >= 0 {
		p.emit(event("text_end", object{"contentIndex": p.textIndex, "content": str(obj(arr(p.output["content"])[p.textIndex])["text"])}))
		p.textIndex = -1
	}
	indices := make([]uint64, 0, len(p.pending))
	for idx := range p.pending {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	for _, idx := range indices {
		tool := p.pending[idx]
		var args any
		if json.Unmarshal([]byte(tool.ArgsBuffer), &args) != nil {
			args = object{"_raw": tool.ArgsBuffer}
		}
		obj(arr(p.output["content"])[tool.ContentIndex])["arguments"] = args
		p.emit(event("toolcall_end", object{"contentIndex": tool.ContentIndex, "toolCallId": tool.ID, "toolName": tool.Name, "toolCall": object{"type": "toolCall", "id": tool.ID, "name": tool.Name, "arguments": args}}))
		delete(p.pending, idx)
	}
}
func (p *streamParser) finish() {
	p.closeBlocks()
	p.emit(event("done", object{"reason": p.output["stopReason"], "message": cloneObject(p.output)}))
}

func streamFreeModel(ctx context.Context, client *http.Client, id string, input raceInput, key, baseURL string, emit func(object)) error {
	body, _ := json.Marshal(buildChatBody(id, input))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cleanURL(baseURL)+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		emit(streamError("error", err.Error()))
		return &modelError{Kind: "other", Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "freerouter")
	req.Header.Set("User-Agent", "freerouter/0.1")
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			emit(streamError("aborted", id+" aborted"))
			return &modelError{Kind: "cancelled"}
		}
		emit(streamError("error", err.Error()))
		return &modelError{Kind: "other", Message: err.Error()}
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status == 402 {
		emit(streamError("error", "OpenRouter API key has insufficient credits."))
		return &modelError{Kind: "fatal", Message: "OpenRouter API key has insufficient credits. Add credits at openrouter.ai/credits."}
	}
	if status == 429 || status >= 500 || status == 400 || status == 422 {
		emit(streamError("error", fmt.Sprintf("%s failed (HTTP %d)", id, status)))
		return &modelError{Kind: "exhausted", ModelID: id, Status: status}
	}
	if status < 200 || status >= 300 {
		msg := fmt.Sprintf("OpenRouter error: %d %s", status, http.StatusText(status))
		emit(streamError("error", msg))
		return &modelError{Kind: "other", Message: msg}
	}
	parser := newStreamParser(id, emit)
	emit(event("start", nil))
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 65*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(line[6:])
		if data == "[DONE]" {
			parser.finish()
			return nil
		}
		var chunk object
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if err := parser.handle(chunk); err != nil {
			parser.closeBlocks()
			emit(streamError("error", err.Error()))
			return err
		}
	}
	if ctx.Err() != nil {
		parser.closeBlocks()
		emit(streamError("aborted", id+" aborted"))
		return &modelError{Kind: "cancelled"}
	}
	if err := scanner.Err(); err != nil {
		parser.closeBlocks()
		emit(streamError("error", err.Error()))
		return &modelError{Kind: "other", Message: err.Error()}
	}
	parser.finish()
	return nil
}
