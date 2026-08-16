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
	// Completions 的 role:tool 只能是字符串，图必须另走 user 的 image_url。
	// 但不能「每条 toolResult 立刻跟一条带图 user」：同一次 assistant.tool_calls
	// 对应的 role:tool 必须连在一起，中间插 user 多数网关会 400。
	//
	// 对齐 pi packages/ai/src/api/openai-completions.ts：
	//   1. 连续 toolResult（同一次并行工具）先全部编成 role:tool（纯文本）。
	//   2. 这一组里的图攒成一条 user，跟在这组 tool 后面。
	//   3. 更早轮次的图仍待在当时那一组后面，不挪到整段对话末尾
	//      （前缀字节稳定，才能命中 prompt cache）。
	// 无图时不插 user，行为和改之前的纯文本路径相同。
	history := replayable(req.Messages)
	for i := 0; i < len(history); i++ {
		m := history[i]
		if m.Role != "toolResult" {
			msgs = append(msgs, toOpenAIMessage(m))
			continue
		}
		var imgs []map[string]any
		for ; i < len(history) && history[i].Role == "toolResult"; i++ {
			tm := history[i]
			msgs = append(msgs, toOpenAIMessage(tm))
			imgs = append(imgs, openAIImageURLs(tm)...)
		}
		i--
		if extra := openAIToolImageFollowup(imgs); extra != nil {
			msgs = append(msgs, extra)
		}
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
	for _, m := range replayable(req.Messages) {
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
		// Responses 允许 function_call_output.output 为 input_text+input_image
		// 数组（pi / Codex 都这么做），图留在这条 output 里，不另插 user，
		// 并行多条 output 才能连在一起。
		return []any{map[string]any{
			"type":    "function_call_output",
			"call_id": m.ToolCallID,
			"output":  responsesToolOutput(m),
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

// AnthropicBody is the Anthropic Messages payload.
func AnthropicBody(req loop.Request) map[string]any {
	var msgs []map[string]any
	// 对齐 pi：连续 toolResult 收成一条 user，里面多个 tool_result。
	// 图放在各自的 tool_result.content 里（Anthropic 允许），不拆成跟班 user。
	history := replayable(req.Messages)
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

// replayable drops assistant turns providers reject on replay (pi transformMessages):
// aborted/error whole turns, and assistants with no text/thinking/toolCall.
// Tool results that belonged to a dropped assistant are dropped too.
// A kept assistant with unmatched tool calls gets a synthetic error result
// so Completions/Anthropic do not 400 on a missing role:tool.
func replayable(msgs []types.Message) []types.Message {
	skipIDs := map[string]bool{}
	for _, m := range msgs {
		if m.Role != "assistant" || !skipAssistant(m) {
			continue
		}
		for _, c := range m.ToolCalls() {
			if c.ID != "" {
				skipIDs[c.ID] = true
			}
		}
	}
	out := make([]types.Message, 0, len(msgs))
	var pending []types.Content
	have := map[string]bool{}
	flushOrphans := func() {
		for _, c := range pending {
			if c.ID == "" || have[c.ID] {
				continue
			}
			out = append(out, types.Message{
				Role:       "toolResult",
				ToolCallID: c.ID,
				ToolName:   c.Name,
				Content:    []types.Content{{Type: "text", Text: "No result provided"}},
				IsError:    true,
			})
		}
		pending = nil
		have = map[string]bool{}
	}
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			if skipAssistant(m) {
				continue
			}
			flushOrphans()
			pending = m.ToolCalls()
			have = map[string]bool{}
			out = append(out, m)
		case "toolResult":
			if skipIDs[m.ToolCallID] {
				continue
			}
			have[m.ToolCallID] = true
			out = append(out, m)
		case "user":
			flushOrphans()
			out = append(out, m)
		default:
			out = append(out, m)
		}
	}
	flushOrphans()
	return out
}

func skipAssistant(m types.Message) bool {
	if m.StopReason == "aborted" || m.StopReason == "error" {
		return true
	}
	return !assistantHasReplayableContent(m)
}

func assistantHasReplayableContent(m types.Message) bool {
	for _, c := range m.Content {
		switch c.Type {
		case "text", "":
			if c.Text != "" {
				return true
			}
		case "thinking":
			if c.Thinking != "" {
				return true
			}
		case "toolCall":
			return true
		}
	}
	return false
}

func toOpenAIMessage(m types.Message) map[string]any {
	switch m.Role {
	case "toolResult":
		text := m.Text()
		if text == "" && hasImage(m) {
			text = "(see attached image)"
		}
		return map[string]any{
			"role":         "tool",
			"tool_call_id": m.ToolCallID,
			"content":      text,
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
		if parts := openAIContentParts(m); parts != nil {
			return map[string]any{"role": "user", "content": parts}
		}
		return map[string]any{"role": "user", "content": m.Text()}
	}
}

func openAIContentParts(m types.Message) []map[string]any {
	var parts []map[string]any
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
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + mime + ";base64," + c.Data,
				},
			})
		case "text", "":
			if c.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": c.Text})
			}
		}
	}
	if !hasMedia {
		return nil
	}
	return parts
}

func hasImage(m types.Message) bool {
	for _, c := range m.Content {
		if c.Type == "image" && c.Data != "" {
			return true
		}
	}
	return false
}

func openAIImageURLs(m types.Message) []map[string]any {
	var imgs []map[string]any
	for _, p := range openAIContentParts(m) {
		if p["type"] == "image_url" {
			imgs = append(imgs, p)
		}
	}
	return imgs
}

func openAIToolImageFollowup(imgs []map[string]any) map[string]any {
	if len(imgs) == 0 {
		return nil
	}
	content := append([]map[string]any{{
		"type": "text",
		"text": "Attached image(s) from tool result:",
	}}, imgs...)
	return map[string]any{"role": "user", "content": content}
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

func anthropicToolResultBlock(m types.Message) map[string]any {
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

func toAnthropicMessage(m types.Message) map[string]any {
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

func anthropicMediaBlocks(m types.Message) []map[string]any {
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
