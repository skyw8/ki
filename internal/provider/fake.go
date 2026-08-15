package provider

import (
	"context"
	"sync"

	"ki/internal/loop"
	"ki/internal/types"
)

// Streamer is the provider-facing alias of loop.Streamer.
type Streamer = loop.Streamer

// Scripted is a test/dev Streamer with canned steps.
type Scripted struct {
	mu    sync.Mutex
	Steps []types.Message
	i     int
}

// Stream returns the next scripted assistant message.
func (s *Scripted) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
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
	return r.Inner.Stream(ctx, req, emit)
}
