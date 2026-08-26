package llmprotocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

var errProviderHTTP = errors.New("provider HTTP error")

func marshalArguments(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<marshal arguments: %v>", err)
	}
	return string(b)
}

func validObjectArgumentsRaw(raw string) string {
	// A length-truncated assistant tool call is still present in session
	// history; only replay valid JSON objects or the next provider request can
	// fail before the model gets the explanatory tool_result.
	if raw == "" {
		return ""
	}
	var value map[string]any
	if json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return ""
	}
	return raw
}

// Stream implements Streamer.
func (l *Client) Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (Message, error) {
	var msg Message
	var err error
	switch l.API {
	case APIAnthropic:
		msg, err = l.streamAnthropic(ctx, req, emit)
	case APIResponses:
		msg, err = l.streamResponses(ctx, req, emit)
	default:
		msg, err = l.streamCompletions(ctx, req, emit)
	}
	return msg, err
}

func mappedThinking(req Request) string {
	if req.ThinkingEffort == "" {
		return ""
	}
	if v, ok := req.ThinkingLevelMap[req.ThinkingEffort]; ok {
		if v == nil {
			return ""
		}
		return *v
	}
	return req.ThinkingEffort
}

// Replayable removes assistant turns that cannot be sent back to a provider
// and synthesizes missing tool results for retained tool calls.
func Replayable(msgs []Message) []Message {
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
	out := make([]Message, 0, len(msgs))
	var pending []Content
	have := map[string]bool{}
	flushOrphans := func() {
		for _, c := range pending {
			if c.ID == "" || have[c.ID] {
				continue
			}
			out = append(out, Message{
				Role:       "toolResult",
				ToolCallID: c.ID,
				ToolName:   c.Name,
				ToolType:   c.ToolType,
				Content:    []Content{{Type: "text", Text: "No result provided"}},
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

func skipAssistant(m Message) bool {
	if m.StopReason == "aborted" || m.StopReason == "error" {
		return true
	}
	return !assistantHasReplayableContent(m)
}

func assistantHasReplayableContent(m Message) bool {
	for _, c := range m.Content {
		switch c.Type {
		case "text", "":
			if c.Text != "" {
				return true
			}
		case "thinking":
			if c.Thinking != "" || c.ThinkingSignature != "" || c.ThinkingData != "" {
				return true
			}
		case "toolCall":
			return true
		}
	}
	return false
}

func (l *Client) oaHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+l.APIKey)
	return h
}

type sseParseResult struct {
	delta    AssistantDelta
	emit     bool
	deltas   []AssistantDelta
	terminal bool
	err      error
}

type sseParser func(event, data string, acc *Message) sseParseResult

func (l *Client) postStream(ctx context.Context, url string, body any, hdr http.Header, emit func(AssistantDelta) error, parse sseParser, requireTerminal bool) (Message, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Message{}, err
	}
	httpReq.Header = hdr
	httpReq.Header.Set("Accept", "text/event-stream")
	res, err := l.Doer.Do(httpReq)
	if err != nil {
		return Message{}, fmt.Errorf("send provider request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		msg := fmt.Sprintf("http %d: %s", res.StatusCode, truncate(string(b), 500))
		return Message{
			Role:         "assistant",
			StopReason:   "error",
			ErrorMessage: msg,
		}, fmt.Errorf("%w: %s", errProviderHTTP, msg)
	}
	acc := Message{Role: "assistant"}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var eventName string
	var dataLines []string
	terminal := false
	stopped := false
	var streamErr error
	dispatch := func() {
		if streamErr != nil || len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if strings.TrimSpace(data) == "[DONE]" {
			stopped = true
			return
		}
		result := parse(eventName, data, &acc)
		if result.err != nil {
			streamErr = result.err
			return
		}
		terminal = terminal || result.terminal
		if emit != nil {
			deltas := result.deltas
			if result.emit {
				deltas = append([]AssistantDelta{result.delta}, deltas...)
			}
			for _, delta := range deltas {
				if err := emit(delta); err != nil {
					streamErr = err
					break
				}
			}
		}
	}
	for sc.Scan() && streamErr == nil && !stopped && !terminal {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			dispatch()
			eventName = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(after)
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			// SSE removes one optional leading space from each data field; all
			// remaining fields are joined with newlines by the SSE contract.
			dataLines = append(dataLines, strings.TrimPrefix(after, " "))
		}
	}
	if streamErr == nil && !stopped && !terminal && len(dataLines) > 0 {
		dispatch()
	}
	if streamErr == nil {
		streamErr = sc.Err()
	}
	if streamErr != nil {
		return acc, streamErr
	}
	if requireTerminal && !terminal {
		return acc, errors.New("provider SSE stream ended before a terminal response event")
	}
	if acc.ErrorMessage != "" {
		return acc, fmt.Errorf("provider response failed: %s", acc.ErrorMessage)
	}
	if acc.StopReason == "" {
		if len(acc.ToolCalls()) > 0 {
			acc.StopReason = "toolUse"
		} else {
			acc.StopReason = "stop"
		}
	}
	if acc.Usage == nil {
		acc.Usage = &Usage{}
	}
	if acc.Usage.TotalTokens == 0 {
		acc.Usage.TotalTokens = acc.Usage.Input + acc.Usage.Output + acc.Usage.CacheRead + acc.Usage.CacheWrite
	}
	return acc, sc.Err()
}

func appendText(m *Message, s string) {
	if s == "" {
		return
	}
	for i := range m.Content {
		if m.Content[i].Type == "text" {
			m.Content[i].Text += s
			return
		}
	}
	m.Content = append(m.Content, Content{Type: "text", Text: s})
}

func appendThinking(m *Message, s string) {
	for i := range m.Content {
		if m.Content[i].Type == "thinking" {
			m.Content[i].Thinking += s
			return
		}
	}
	m.Content = append(m.Content, Content{Type: "thinking", Thinking: s})
}

func appendToolCallDelta(m *Message, id, itemID, name, argsDelta string, index int) {
	c := findOrAddToolCall(m, id, itemID, name, index)
	appendToolArgs(c, argsDelta)
}

func findOrAddToolCall(m *Message, id, itemID, name string, index int) *Content {
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
		for i := range slices.Backward(m.Content) {
			if m.Content[i].Type == "toolCall" {
				if name != "" {
					m.Content[i].Name = name
				}
				return &m.Content[i]
			}
		}
	}
	m.Content = append(m.Content, Content{
		Type:      "toolCall",
		ID:        id,
		ItemID:    itemID,
		Name:      name,
		Arguments: map[string]any{},
	})
	return &m.Content[len(m.Content)-1]
}

func appendToolArgs(c *Content, delta string) {
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
