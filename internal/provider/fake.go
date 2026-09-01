package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"ki/internal/loop"
	"ki/internal/types"
)

// HoldToken in the latest user message makes Scripted wait until the
// request context is canceled. Playwright and CLI e2e use it to keep a
// fake run busy; it is a no-op for the live provider.
const HoldToken = "e2e-hold"

// WriteEnvToken makes the default fake assistant issue a Write of .env so
// extension intercept e2e can prove the call is blocked.
const WriteEnvToken = "e2e-write-env" //nolint:gosec // e2e prompt marker, not a credential

// SleepInterceptToken makes the default fake assistant issue a Bash command
// that extension intercept e2e holds until abort.
const SleepInterceptToken = "e2e-sleep-intercept" //nolint:gosec // e2e prompt marker, not a credential

// MarkdownToken makes the default fake assistant emit a GFM table and a
// mermaid fence so WebUI Playwright can exercise those renderers without a
// live model.
const MarkdownToken = "e2e-markdown"

// MarkdownFixture is the canned assistant text for MarkdownToken.
const MarkdownFixture = "| Col A | Col B |\n| --- | --- |\n| 1 | 2 |\n\n```mermaid\nflowchart LR\n  Start --> End\n```\n"

// Scripted is a test/dev Streamer with canned steps.
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

// Stream returns the next scripted assistant message.
func (s *Scripted) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	if lastUserHolds(req) {
		<-ctx.Done()
		return types.Message{}, ctx.Err()
	}
	if lastMessageIsUserContaining(req, WriteEnvToken) {
		m := types.Message{
			Role: "assistant",
			Content: []types.Content{{
				Type: "toolCall", ID: "call-write-env", Name: "Write",
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
				Type: "toolCall", ID: "call-sleep", Name: "Bash",
				Arguments: map[string]any{"command": "SLEEP_INTERCEPT"},
			}},
			StopReason: "toolUse",
			Provider:   req.Provider,
			Model:      req.Model,
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
			Usage:      &types.Usage{Input: 8, Output: 2, TotalTokens: 10},
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
			Usage:      &types.Usage{Input: 8, Output: 2, TotalTokens: 10},
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

// LastRequest is filled by Recording.
type LastRequest struct {
	Req loop.Request
}

// Recording wraps a Streamer and stores the last Request.
type Recording struct {
	Inner Streamer
	Last  loop.Request
}

// Stream implements loop.Streamer.
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
