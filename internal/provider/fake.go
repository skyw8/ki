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

// HoldToken in the latest user message makes Scripted wait until the
// request context is canceled. Playwright and CLI e2e use it to keep a
// fake run busy; it is a no-op for the live provider.
const HoldToken = "e2e-hold"

// DelayTokenPrefix keeps a fake run busy for a bounded time and then completes
// it normally, unlike HoldToken which only ever ends through abort. The delay
// is the decimal millisecond count right after the prefix, e.g. `e2e-delay-800`.
// WebUI e2e uses it to finish a run after the user switched sessions. A no-op
// for the live provider.
const DelayTokenPrefix = "e2e-delay-"

// WriteEnvToken makes the default fake assistant issue a Write of .env so
// extension intercept e2e can prove the call is blocked.
const WriteEnvToken = "e2e-write-env" //nolint:gosec // e2e prompt marker, not a credential

// SleepInterceptToken makes the default fake assistant issue a Bash command
// that extension intercept e2e holds until abort.
const SleepInterceptToken = "e2e-sleep-intercept" //nolint:gosec // e2e prompt marker, not a credential

// StreamTokenPrefix makes the fake assistant emit `<n>` streaming chunks of a
// growing message, each carrying the whole accumulated partial, with a short
// pause between them so the run stays live while a test watches it. It is how
// WebUI tests exercise the replay and render path a long streaming turn
// produces, without a live model. A no-op for the live provider.
const StreamTokenPrefix = "e2e-stream-"

// BlocksTokenPrefix makes the fake assistant emit `<n>` markdown paragraphs, one
// per chunk, each followed by a blank line. Unlike StreamTokenPrefix (one long
// unbroken list) this is the shape that makes Streamdown re-lex the whole message
// per delta, which is what the settled-prefix split in the WebUI avoids. A no-op
// for the live provider.
const BlocksTokenPrefix = "e2e-blocks-"

// MarkdownToken makes the default fake assistant emit a GFM table, a mermaid
// fence, and a plantuml fence so WebUI Playwright can exercise those renderers
// without a live model.
const MarkdownToken = "e2e-markdown"

// BashTokenPrefix makes the fake assistant run the rest of the user message
// through the Bash tool and report back, so a CLI e2e can assert what a shell
// command actually resolves on this host. Unlike the intercept tokens this
// probe completes its round trip, so it only fires while the request has no
// tool result yet.
const BashTokenPrefix = "e2e-bash:"

// MarkdownFixture is the canned assistant text for MarkdownToken.
const MarkdownFixture = "| Col A | Col B |\n| --- | --- |\n| 1 | 2 |\n\n```mermaid\nflowchart LR\n  Start --> End\n```\n\n```plantuml\n@startuml\nAlice -> Bob: hi\n@enduml\n```\n"

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

// lastUserDelay parses `e2e-delay-<ms>` from the latest user message; 0 means no
// delay (missing, malformed, or non-positive).
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

// lastUserChunks parses `<prefix><n>` from the latest user message; 0 means the
// token is missing or malformed, or the request already carries a tool result.
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

// lastUserStreamChunks parses `e2e-stream-<n>`: n lines of one growing list block.
func lastUserStreamChunks(req loop.Request) int { return lastUserChunks(req, StreamTokenPrefix) }

// lastUserBlockChunks parses `e2e-blocks-<n>`: n paragraphs, blank-line separated.
func lastUserBlockChunks(req loop.Request) int { return lastUserChunks(req, BlocksTokenPrefix) }

// lastUserBashProbe returns the command after BashTokenPrefix in the latest
// user message. It reports false once the request already carries a tool
// result, so the probe round trip terminates instead of looping.
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

// Stream returns the next scripted assistant message.
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
			// Every chunk repeats the whole partial, exactly like the provider
			// adapters: that repetition is what makes a replay quadratic unless
			// the server trims it.
			// A markdown list item per chunk: the text grows to a realistic size,
			// so a client that re-parses it per delta pays the real cost.
			line := fmt.Sprintf("- item %d %s\n", i, strings.Repeat("x", 110))
			acc.WriteString(line)
			m.Content = []types.Content{{Type: "text", Text: acc.String()}}
			if err := emit(loop.AssistantDelta{Type: "text_delta", Delta: line, Partial: m}); err != nil {
				return m, err
			}
			if i%8 == 0 {
				// Keep the run busy for a moment so a test can observe it live.
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
