package provider

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ki/internal/loop"
	"ki/internal/types"
)

const HoldToken = "e2e-hold"

const DelayTokenPrefix = "e2e-delay-"

const WriteEnvToken = "e2e-write-env" //nolint:gosec // e2e prompt marker, not a credential

const SleepInterceptToken = "e2e-sleep-intercept" //nolint:gosec // e2e prompt marker, not a credential

const StreamTokenPrefix = "e2e-stream-"

const BlocksTokenPrefix = "e2e-blocks-"

const MarkdownToken = "e2e-markdown"

const BashTokenPrefix = "e2e-bash:"

const MarkdownFixture = "| Col A | Col B |\n| --- | --- |\n| 1 | 2 |\n\n```mermaid\nflowchart LR\n  Start --> End\n```\n\n```plantuml\n@startuml\nAlice -> Bob: hi\n@enduml\n```\n"

type Scripted struct {
	mu    sync.Mutex
	Steps []types.Message
	i     int
}

func lastUserHolds(req loop.Request) bool {
	return lastUserContains(req, HoldToken)
}

func lastUserContains(req loop.Request, token string) bool {
	for _, msg := range slices.Backward(req.Messages) {
		if msg.Role == "user" {
			return strings.Contains(msg.Text(), token)
		}
	}
	return false
}

func lastMessageIsUserContaining(req loop.Request, token string) bool {
	if len(req.Messages) == 0 {
		return false
	}
	last := req.Messages[len(req.Messages)-1]
	return last.Role == "user" && strings.Contains(last.Text(), token)
}

func lastUserDelay(req loop.Request) time.Duration {
	text := ""
	for _, msg := range slices.Backward(req.Messages) {
		if msg.Role == "user" {
			text = msg.Text()
			break
		}
	}
	i := strings.Index(text, DelayTokenPrefix)
	if i < 0 {
		return 0
	}
	rest := text[i+len(DelayTokenPrefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	ms, err := strconv.Atoi(rest[:end])
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func lastUserChunks(req loop.Request, prefix string) int {
	text := ""
	for _, msg := range slices.Backward(req.Messages) {
		if msg.Role == "user" {
			text = msg.Text()
			break
		}
	}
	i := strings.Index(text, prefix)
	if i < 0 {
		return 0
	}
	rest := text[i+len(prefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil || n <= 0 {
		return 0
	}
	if n > 2000 {
		n = 2000
	}
	return n
}

func lastUserStreamChunks(req loop.Request) int { return lastUserChunks(req, StreamTokenPrefix) }

func lastUserBlockChunks(req loop.Request) int { return lastUserChunks(req, BlocksTokenPrefix) }

func lastUserBashProbe(req loop.Request) (string, bool) {
	for _, msg := range req.Messages {
		if msg.Role == "toolResult" {
			return "", false
		}
	}
	for _, msg := range slices.Backward(req.Messages) {
		if msg.Role != "user" {
			continue
		}
		text := msg.Text()
		i := strings.Index(text, BashTokenPrefix)
		if i < 0 {
			return "", false
		}
		command := strings.TrimSpace(text[i+len(BashTokenPrefix):])
		return command, command != ""
	}
	return "", false
}

func (s *Scripted) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	if lastUserHolds(req) {
		<-ctx.Done()
		return types.Message{}, ctx.Err()
	}
	if delay := lastUserDelay(req); delay > 0 {
		select {
		case <-ctx.Done():
			return types.Message{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if command, ok := lastUserBashProbe(req); ok {
		return types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "call-bash-probe", Name: "exec_command",
				Arguments: map[string]any{"cmd": command},
			}},
			StopReason: "toolUse",
			Provider:   req.Provider,
			Model:      req.Model,
		}, ctx.Err()
	}
	if lastMessageIsUserContaining(req, WriteEnvToken) {
		m := types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "call-write-env", Name: "write",
				Arguments: map[string]any{"path": ".env", "content": "SECRET=1"},
			}},
			StopReason: "toolUse",
			Provider:   req.Provider,
			Model:      req.Model,
		}
		return m, ctx.Err()
	}
	if lastMessageIsUserContaining(req, SleepInterceptToken) {
		m := types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "call-sleep", Name: "exec_command",
				Arguments: map[string]any{"cmd": "SLEEP_INTERCEPT"},
			}},
			StopReason: "toolUse",
			Provider:   req.Provider,
			Model:      req.Model,
		}
		return m, ctx.Err()
	}
	if n := lastUserStreamChunks(req); n > 0 {
		m := types.Message{Role: "assistant", StopReason: "stop", Provider: req.Provider, Model: req.Model}
		var acc strings.Builder
		for i := 0; i < n; i++ {
			line := fmt.Sprintf("- item %d %s\n", i, strings.Repeat("x", 110))
			acc.WriteString(line)
			m.Content = []types.Content{{Type: "text", Text: acc.String()}}
			if err := emit(loop.AssistantDelta{Type: "text_delta", Delta: line, Partial: m}); err != nil {
				return m, err
			}
			if i%8 == 0 {
				select {
				case <-ctx.Done():
					return m, ctx.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
		}
		return m, ctx.Err()
	}
	if n := lastUserBlockChunks(req); n > 0 {
		m := types.Message{Role: "assistant", StopReason: "stop", Provider: req.Provider, Model: req.Model}
		var acc strings.Builder
		for i := 0; i < n; i++ {
			block := fmt.Sprintf("Paragraph %d: %s\n\n", i, strings.Repeat("lorem ipsum dolor ", 7))
			acc.WriteString(block)
			m.Content = []types.Content{{Type: "text", Text: acc.String()}}
			if err := emit(loop.AssistantDelta{Type: "text_delta", Delta: block, Partial: m}); err != nil {
				return m, err
			}
			if i%8 == 0 {
				select {
				case <-ctx.Done():
					return m, ctx.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
		}
		return m, ctx.Err()
	}
	if lastMessageIsUserContaining(req, MarkdownToken) {
		m := types.Message{
			Role:       "assistant",
			Content:    []types.Content{{Type: "text", Text: MarkdownFixture}},
			StopReason: "stop",
			Provider:   req.Provider,
			Model:      req.Model,
			Usage:      &types.Usage{Input: 8, Output: 2, CacheRead: 90, TotalTokens: 100},
		}
		_ = emit(loop.AssistantDelta{Type: "text_delta", Delta: MarkdownFixture, Partial: m})
		return m, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.Steps) {
		m := types.Message{
			Role:       "assistant",
			Content:    []types.Content{{Type: "text", Text: "ok"}},
			StopReason: "stop",
			Provider:   req.Provider,
			Model:      req.Model,
			Usage:      &types.Usage{Input: 8, Output: 2, CacheRead: 90, TotalTokens: 100},
		}
		_ = emit(loop.AssistantDelta{Type: "text_delta", Delta: "ok", Partial: m})
		return m, nil
	}
	m := s.Steps[s.i]
	s.i++
	m.Role = "assistant"
	if m.StopReason == "" {
		if len(m.ToolCalls()) > 0 {
			m.StopReason = "toolUse"
		} else {
			m.StopReason = "stop"
		}
	}
	text := m.Text()
	if text != "" {
		_ = emit(loop.AssistantDelta{Type: "text_delta", Delta: text, Partial: m})
	}
	return m, ctx.Err()
}

type LastRequest struct {
	Req loop.Request
}

type Recording struct {
	Inner Streamer
	Last  loop.Request
}

func (r *Recording) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	r.Last = req
	if r.Inner == nil {
		return (&Scripted{}).Stream(ctx, req, emit)
	}
	msg, err := r.Inner.Stream(ctx, req, emit)
	if err != nil {
		return msg, fmt.Errorf("stream wrapped provider: %w", err)
	}
	return msg, nil
}
