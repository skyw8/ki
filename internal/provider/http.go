package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ki/internal/loop"
	"ki/internal/types"
)

// HTTPDoer is the transport used by live streamers.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Live streams via one of the three protocols.
type Live struct {
	Doer   HTTPDoer
	APIKey string
	Base   string
	API    string // completions | responses | anthropic
}

// NewLive builds a live streamer. doer nil uses http.DefaultClient.
func NewLive(api, base, key string, doer HTTPDoer) *Live {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Live{Doer: doer, APIKey: key, Base: strings.TrimRight(base, "/"), API: api}
}

// Stream implements loop.Streamer.
func (l *Live) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	switch l.API {
	case "anthropic":
		return l.streamAnthropic(ctx, req, emit)
	case "responses":
		return l.streamResponses(ctx, req, emit)
	default:
		return l.streamCompletions(ctx, req, emit)
	}
}

// CompletionsBody is the OpenAI chat completions payload (exported for tests).
func CompletionsBody(req loop.Request) map[string]any {
	msgs := []map[string]any{}
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, toOpenAIMessage(m))
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = tools
	}
	return body
}

// ResponsesBody is the OpenAI Responses API payload.
func ResponsesBody(req loop.Request) map[string]any {
	var input []any
	for _, m := range req.Messages {
		input = append(input, toResponsesItems(m)...)
	}
	body := map[string]any{
		"model":  req.Model,
		"input":  input,
		"stream": true,
	}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
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

func toResponsesItems(m types.Message) []any {
	switch m.Role {
	case "toolResult":
		return []any{map[string]any{
			"type":    "function_call_output",
			"call_id": m.ToolCallID,
			"output":  m.Text(),
		}}
	case "assistant":
		var items []any
		var text string
		for _, c := range m.Content {
			switch c.Type {
			case "text", "":
				text += c.Text
			case "toolCall":
				args, _ := json.Marshal(c.Arguments)
				item := map[string]any{
					"type":      "function_call",
					"call_id":   c.ID,
					"name":      c.Name,
					"arguments": string(args),
				}
				if c.ItemID != "" {
					item["id"] = c.ItemID
				}
				items = append(items, item)
			}
		}
		if text != "" {
			items = append([]any{map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": text}},
			}}, items...)
		}
		return items
	default:
		return []any{map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": m.Text()}},
		}}
	}
}

// AnthropicBody is the Anthropic Messages payload.
func AnthropicBody(req loop.Request) map[string]any {
	var msgs []map[string]any
	for _, m := range req.Messages {
		msgs = append(msgs, toAnthropicMessage(m))
	}
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": 8192,
		"messages":   msgs,
		"stream":     true,
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
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			})
		}
		body["tools"] = tools
	}
	return body
}

func toOpenAIMessage(m types.Message) map[string]any {
	switch m.Role {
	case "toolResult":
		return map[string]any{
			"role":         "tool",
			"tool_call_id": m.ToolCallID,
			"content":      m.Text(),
		}
	case "assistant":
		out := map[string]any{"role": "assistant"}
		var text string
		var calls []map[string]any
		for _, c := range m.Content {
			switch c.Type {
			case "text", "":
				text += c.Text
			case "thinking":
				// OpenAI-compat reasoning_content
				out["reasoning_content"] = c.Thinking
			case "toolCall":
				args, _ := json.Marshal(c.Arguments)
				calls = append(calls, map[string]any{
					"id":   c.ID,
					"type": "function",
					"function": map[string]any{
						"name":      c.Name,
						"arguments": string(args),
					},
				})
			}
		}
		if text != "" {
			out["content"] = text
		}
		if len(calls) > 0 {
			out["tool_calls"] = calls
		}
		return out
	default:
		return map[string]any{"role": "user", "content": m.Text()}
	}
}

func toAnthropicMessage(m types.Message) map[string]any {
	if m.Role == "toolResult" {
		return map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Text(),
				"is_error":    m.IsError,
			}},
		}
	}
	if m.Role == "assistant" {
		var blocks []map[string]any
		for _, c := range m.Content {
			switch c.Type {
			case "thinking":
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": c.Thinking})
			case "toolCall":
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    c.ID,
					"name":  c.Name,
					"input": c.Arguments,
				})
			default:
				if c.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": c.Text})
				}
			}
		}
		return map[string]any{"role": "assistant", "content": blocks}
	}
	return map[string]any{"role": "user", "content": m.Text()}
}

func (l *Live) streamCompletions(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	url := l.Base + "/chat/completions"
	return l.postStream(ctx, url, CompletionsBody(req), l.oaHeaders(), emit, parseCompletionsSSE)
}

func (l *Live) streamResponses(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	url := l.Base + "/responses"
	return l.postStream(ctx, url, ResponsesBody(req), l.oaHeaders(), emit, parseResponsesSSE)
}

func (l *Live) streamAnthropic(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	url := l.Base + "/v1/messages"
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("x-api-key", l.APIKey)
	h.Set("anthropic-version", "2023-06-01")
	h.Set("anthropic-beta", "prompt-caching-2024-07-31")
	return l.postStream(ctx, url, AnthropicBody(req), h, emit, parseAnthropicSSE)
}

func (l *Live) oaHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+l.APIKey)
	return h
}

type sseParser func(event, data string, acc *types.Message) (loop.AssistantDelta, bool)

func (l *Live) postStream(ctx context.Context, url string, body any, hdr http.Header, emit func(loop.AssistantDelta) error, parse sseParser) (types.Message, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return types.Message{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return types.Message{}, err
	}
	httpReq.Header = hdr
	res, err := l.Doer.Do(httpReq)
	if err != nil {
		return types.Message{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return types.Message{
			Role:         "assistant",
			StopReason:   "error",
			ErrorMessage: fmt.Sprintf("http %d: %s", res.StatusCode, truncate(string(b), 500)),
		}, fmt.Errorf("http %d", res.StatusCode)
	}
	acc := types.Message{Role: "assistant"}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var evName string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			evName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		d, ok := parse(evName, data, &acc)
		if ok {
			_ = emit(d)
		}
	}
	if acc.StopReason == "" {
		if len(acc.ToolCalls()) > 0 {
			acc.StopReason = "toolUse"
		} else {
			acc.StopReason = "stop"
		}
	}
	if acc.Usage == nil {
		acc.Usage = &types.Usage{}
	}
	acc.Usage.TotalTokens = acc.Usage.Input + acc.Usage.Output + acc.Usage.CacheRead + acc.Usage.CacheWrite
	return acc, sc.Err()
}

func parseCompletionsSSE(_, data string, acc *types.Message) (loop.AssistantDelta, bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			PromptCacheHit   int `json:"prompt_cache_hit_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return loop.AssistantDelta{}, false
	}
	if chunk.Usage != nil {
		acc.Usage = &types.Usage{
			Input:     chunk.Usage.PromptTokens,
			Output:    chunk.Usage.CompletionTokens,
			CacheRead: chunk.Usage.PromptCacheHit,
		}
	}
	if len(chunk.Choices) == 0 {
		return loop.AssistantDelta{}, false
	}
	d := chunk.Choices[0].Delta
	if d.Content != "" {
		appendText(acc, d.Content)
		return loop.AssistantDelta{Type: "text_delta", Delta: d.Content, Partial: *acc}, true
	}
	if d.ReasoningContent != "" {
		appendThinking(acc, d.ReasoningContent)
		return loop.AssistantDelta{Type: "thinking_delta", Delta: d.ReasoningContent, Partial: *acc}, true
	}
	for _, tc := range d.ToolCalls {
		idx := -1
		if tc.Index != nil {
			idx = *tc.Index
		}
		appendToolCallDelta(acc, tc.ID, "", tc.Function.Name, tc.Function.Arguments, idx)
	}
	switch chunk.Choices[0].FinishReason {
	case "tool_calls":
		acc.StopReason = "toolUse"
	case "length":
		acc.StopReason = "length"
	case "stop":
		acc.StopReason = "stop"
	}
	return loop.AssistantDelta{Type: "text_delta", Partial: *acc}, d.Content != "" || d.ReasoningContent != ""
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
			if t, _ := item["type"].(string); t == "function_call" {
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
			}
		}
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
				acc.Usage = &types.Usage{
					Input:       asInt(u["input_tokens"]),
					Output:      asInt(u["output_tokens"]),
					CacheRead:   asInt(u["cached_tokens"]),
					TotalTokens: asInt(u["total_tokens"]),
				}
			}
		}
	}
	return loop.AssistantDelta{}, false
}

func parseAnthropicSSE(event, data string, acc *types.Message) (loop.AssistantDelta, bool) {
	var obj map[string]any
	if json.Unmarshal([]byte(data), &obj) != nil {
		return loop.AssistantDelta{}, false
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		typ = event
	}
	switch typ {
	case "content_block_delta":
		if delta, ok := obj["delta"].(map[string]any); ok {
			if t, _ := delta["type"].(string); t == "text_delta" {
				text, _ := delta["text"].(string)
				appendText(acc, text)
				return loop.AssistantDelta{Type: "text_delta", Delta: text, Partial: *acc}, true
			}
			if t, _ := delta["type"].(string); t == "thinking_delta" {
				th, _ := delta["thinking"].(string)
				appendThinking(acc, th)
				return loop.AssistantDelta{Type: "thinking_delta", Delta: th, Partial: *acc}, true
			}
			if t, _ := delta["type"].(string); t == "input_json_delta" {
				partial, _ := delta["partial_json"].(string)
				appendToolCallDelta(acc, "", "", "", partial, -1)
			}
		}
	case "content_block_start":
		if blk, ok := obj["content_block"].(map[string]any); ok {
			if t, _ := blk["type"].(string); t == "tool_use" {
				id, _ := blk["id"].(string)
				name, _ := blk["name"].(string)
				c := findOrAddToolCall(acc, id, "", name, -1)
				if input, ok := blk["input"]; ok && input != nil {
					switch v := input.(type) {
					case map[string]any:
						if len(v) > 0 {
							c.Arguments = v
							raw, _ := json.Marshal(v)
							c.ArgumentsRaw = string(raw)
						}
					case string:
						appendToolArgs(c, v)
					}
				}
			}
		}
	case "message_delta":
		if u, ok := obj["usage"].(map[string]any); ok {
			if acc.Usage == nil {
				acc.Usage = &types.Usage{}
			}
			acc.Usage.Output = asInt(u["output_tokens"])
			acc.Usage.CacheRead = asInt(u["cache_read_input_tokens"])
			acc.Usage.CacheWrite = asInt(u["cache_creation_input_tokens"])
		}
		if delta, ok := obj["delta"].(map[string]any); ok {
			if sr, _ := delta["stop_reason"].(string); sr == "tool_use" {
				acc.StopReason = "toolUse"
			}
		}
	case "message_start":
		if msg, ok := obj["message"].(map[string]any); ok {
			if u, ok := msg["usage"].(map[string]any); ok {
				acc.Usage = &types.Usage{
					Input:      asInt(u["input_tokens"]),
					CacheRead:  asInt(u["cache_read_input_tokens"]),
					CacheWrite: asInt(u["cache_creation_input_tokens"]),
				}
			}
		}
	}
	return loop.AssistantDelta{}, false
}

func appendText(m *types.Message, s string) {
	if s == "" {
		return
	}
	for i := range m.Content {
		if m.Content[i].Type == "text" {
			m.Content[i].Text += s
			return
		}
	}
	m.Content = append(m.Content, types.Content{Type: "text", Text: s})
}

func appendThinking(m *types.Message, s string) {
	for i := range m.Content {
		if m.Content[i].Type == "thinking" {
			m.Content[i].Thinking += s
			return
		}
	}
	m.Content = append(m.Content, types.Content{Type: "thinking", Thinking: s})
}

func appendToolCallDelta(m *types.Message, id, itemID, name, argsDelta string, index int) {
	c := findOrAddToolCall(m, id, itemID, name, index)
	appendToolArgs(c, argsDelta)
}

func findOrAddToolCall(m *types.Message, id, itemID, name string, index int) *types.Content {
	if id != "" {
		for i := range m.Content {
			if m.Content[i].Type == "toolCall" && (m.Content[i].ID == id || m.Content[i].ItemID == id) {
				if name != "" {
					m.Content[i].Name = name
				}
				if itemID != "" {
					m.Content[i].ItemID = itemID
				}
				return &m.Content[i]
			}
		}
	}
	if itemID != "" {
		for i := range m.Content {
			if m.Content[i].Type == "toolCall" && (m.Content[i].ItemID == itemID || m.Content[i].ID == itemID) {
				if name != "" {
					m.Content[i].Name = name
				}
				if id != "" {
					m.Content[i].ID = id
				}
				return &m.Content[i]
			}
		}
	}
	if index >= 0 {
		n := 0
		for i := range m.Content {
			if m.Content[i].Type != "toolCall" {
				continue
			}
			if n == index {
				if id != "" {
					m.Content[i].ID = id
				}
				if name != "" {
					m.Content[i].Name = name
				}
				if itemID != "" {
					m.Content[i].ItemID = itemID
				}
				return &m.Content[i]
			}
			n++
		}
	}
	if id == "" && itemID == "" && index < 0 {
		for i := len(m.Content) - 1; i >= 0; i-- {
			if m.Content[i].Type == "toolCall" {
				if name != "" {
					m.Content[i].Name = name
				}
				return &m.Content[i]
			}
		}
	}
	m.Content = append(m.Content, types.Content{
		Type:      "toolCall",
		ID:        id,
		ItemID:    itemID,
		Name:      name,
		Arguments: map[string]any{},
	})
	return &m.Content[len(m.Content)-1]
}

func appendToolArgs(c *types.Content, delta string) {
	if c == nil || delta == "" {
		return
	}
	c.ArgumentsRaw += delta
	var dst map[string]any
	if json.Unmarshal([]byte(c.ArgumentsRaw), &dst) == nil {
		c.Arguments = dst
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
