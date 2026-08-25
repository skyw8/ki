package extension

import (
	"context"
	"log/slog"
	"sync"

	"ki/internal/loop"
	"ki/internal/types"
)

type namedInterceptor struct {
	name       string
	failClosed bool
	points     []string
	hasHook    bool
	inner      Interceptor
}

func (n namedInterceptor) has(point string) bool { return hasPoint(n.points, point) }

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
	fail := func(name, cap, code, message string) {
		skipped.mark(name)
		if onErr != nil {
			onErr(name, cap, code, message)
		}
		slog.Info("extension intercept skipped", "extension", name, "capability", cap, "code", code)
	}
	return loop.Hooks{
		BeforeRun: func(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
			for _, it := range items {
				if skipped.has(it.name) || !it.has(InterceptProvider) {
					continue
				}
				next, nextMsgs, err := it.inner.BeforeRun(ctx, system, msgs)
				if err != nil {
					fail(it.name, string(CapIntercept), "before_run", err.Error())
					continue
				}
				system, msgs = next, nextMsgs
			}
			return system, msgs, nil
		},
		TransformContext: func(ctx context.Context, msgs []types.Message) ([]types.Message, error) {
			for _, it := range items {
				if skipped.has(it.name) || !it.has(InterceptProvider) {
					continue
				}
				next, err := it.inner.TransformContext(ctx, msgs)
				if err != nil {
					fail(it.name, string(CapIntercept), "transform_context", err.Error())
					continue
				}
				msgs = next
			}
			return msgs, nil
		},
		BeforeTool: func(ctx context.Context, name string, args map[string]any) (map[string]any, bool, string, bool, error) {
			call := ToolCall{Name: name, Args: args}
			for _, it := range items {
				if skipped.has(it.name) || !it.has(InterceptTool) {
					continue
				}
				next, block, err := it.inner.BeforeTool(ctx, call)
				if err != nil {
					if it.failClosed {
						return args, true, "extension " + it.name + " failed closed", false, nil
					}
					fail(it.name, string(CapIntercept), "before_tool", err.Error())
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
				if skipped.has(it.name) || !it.has(InterceptTool) {
					continue
				}
				next, err := it.inner.AfterTool(ctx, call, patch)
				if err != nil {
					fail(it.name, string(CapIntercept), "after_tool", err.Error())
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

func RedactEvent(ev loop.Event, sessionID string) Event {
	return Event{
		Type:       string(ev.Type),
		SessionID:  sessionID,
		ToolCallID: ev.ToolCallID,
		ToolName:   ev.ToolName,
		IsError:    ev.IsError,
		Reason:     ev.Reason,
		OK:         ev.OK,
		Provider:   ev.Provider,
		Model:      ev.Model,
	}
}
