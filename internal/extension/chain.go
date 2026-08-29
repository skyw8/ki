package extension

import (
	"context"
	"log/slog"
	"maps"
	"sync"

	"ki/internal/loop"
	"ki/internal/types"
)

type namedInterceptor struct {
	name       string
	failClosed bool
	syncEvents map[string]bool
	inner      Interceptor
}

func (n namedInterceptor) hasSync(event string) bool { return n.syncEvents[event] }

// skipSet is occupy-wide: a failed interceptor is omitted from later hook
// and provider intercept points in the same occupy (BeforeTool skip still
// applies to BeforeProvider, and vice versa).
type skipSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func newSkipSet() *skipSet { return &skipSet{m: map[string]bool{}} }

func (s *skipSet) has(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[name]
}

func (s *skipSet) mark(name string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.m[name] = true
	s.mu.Unlock()
}

// ComposeHooks builds occupy-scoped loop.Hooks with a private skip set.
// Manager.Occupy shares one skipSet with the occupy Streamer.
func ComposeHooks(items []namedInterceptor, onErr func(name, capability, code, message string)) loop.Hooks {
	return composeHooks(items, newSkipSet(), onErr)
}

func composeHooks(items []namedInterceptor, skipped *skipSet, onErr func(name, capability, code, message string)) loop.Hooks {
	if skipped == nil {
		skipped = newSkipSet()
	}
	fail := func(name, capability, code, message string) {
		skipped.mark(name)
		if onErr != nil {
			onErr(name, capability, code, message)
		}
		slog.Info("extension lifecycle skipped", "extension", name, "capability", capability, "code", code)
	}
	return loop.Hooks{
		BeforeRun: func(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
			for _, it := range items {
				if skipped.has(it.name) || !it.hasSync(EventBeforeAgentStart) {
					continue
				}
				next, nextMsgs, err := it.inner.BeforeRun(ctx, system, msgs)
				if err != nil {
					fail(it.name, string(CapLifecycle), EventBeforeAgentStart, err.Error())
					continue
				}
				system, msgs = next, nextMsgs
			}
			return system, msgs, nil
		},
		TransformContext: func(ctx context.Context, msgs []types.Message) ([]types.Message, error) {
			for _, it := range items {
				if skipped.has(it.name) || !it.hasSync(EventContext) {
					continue
				}
				next, err := it.inner.TransformContext(ctx, msgs)
				if err != nil {
					fail(it.name, string(CapLifecycle), EventContext, err.Error())
					continue
				}
				msgs = next
			}
			return msgs, nil
		},
		BeforeTool: func(ctx context.Context, name string, args map[string]any) (map[string]any, bool, string, bool, error) {
			call := ToolCall{Name: name, Args: args}
			for _, it := range items {
				if skipped.has(it.name) || !it.hasSync(EventToolCall) {
					continue
				}
				next, block, err := it.inner.BeforeTool(ctx, call)
				if err != nil {
					if it.failClosed {
						return args, true, "extension " + it.name + " failed closed", false, nil
					}
					fail(it.name, string(CapLifecycle), EventToolCall, err.Error())
					continue
				}
				if block != nil {
					return next.Args, true, block.Reason, block.Terminate, nil
				}
				call = next
			}
			return call.Args, false, "", false, nil
		},
		AfterTool: func(ctx context.Context, name string, args map[string]any, res loop.ToolResult) (loop.ToolResult, error) {
			patch := ResultPatch{Content: res.Content, Details: res.Details, IsError: boolPtr(res.IsError), Terminate: boolPtr(res.Terminate)}
			call := ToolCall{Name: name, Args: args}
			for _, it := range items {
				if skipped.has(it.name) || !it.hasSync(EventToolResult) {
					continue
				}
				next, err := it.inner.AfterTool(ctx, call, patch)
				if err != nil {
					fail(it.name, string(CapLifecycle), EventToolResult, err.Error())
					continue
				}
				patch = next
			}
			if patch.Content != nil {
				res.Content = patch.Content
			}
			if patch.Details != nil {
				res.Details = patch.Details
			}
			if patch.IsError != nil {
				res.IsError = *patch.IsError
			}
			if patch.Terminate != nil {
				res.Terminate = *patch.Terminate
			}
			return res, nil
		},
	}
}

func boolPtr(v bool) *bool { return &v }

// RedactEvent copies a loop event into the extension-facing Event shape.
func RedactEvent(ev loop.Event, sessionID string) Event {
	out := Event{
		Type:       string(ev.Type),
		SessionID:  sessionID,
		RunID:      ev.RunID,
		Timestamp:  ev.Timestamp,
		ToolCallID: ev.ToolCallID,
		ToolName:   ev.ToolName,
		IsError:    ev.IsError,
		DurationMs: ev.DurationMs,
		Reason:     ev.Reason,
		OK:         ev.OK,
		Provider:   ev.Provider,
		Model:      ev.Model,
		External:   cloneStringMap(ev.External),
	}
	if ev.Message != nil {
		out.Role = ev.Message.Role
		out.Text = ev.Message.Text()
		out.StopReason = ev.Message.StopReason
		out.ErrorMessage = ev.Message.ErrorMessage
		if out.Timestamp == 0 {
			out.Timestamp = ev.Message.Timestamp
		}
		if ev.Message.Role == "toolResult" && out.DurationMs == 0 {
			out.DurationMs = ev.Message.DurationMs
		}
		out.IsError = out.IsError || ev.Message.IsError || ev.Message.StopReason == "error"
	}
	if ev.AssistantMessageEvent != nil && ev.AssistantMessageEvent.Delta != "" {
		out.Text = ev.AssistantMessageEvent.Delta
	}
	if ev.ToolName != "" {
		out.ToolTitle = ev.ToolName
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
