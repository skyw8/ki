package loop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

type streamedTool struct {
	call    types.Content
	done    chan struct{}
	outcome dispatchedTool
}

type streamTools struct {
	ctx        context.Context
	cancel     context.CancelFunc
	dispatcher *ToolDispatcher
	pending    map[string]*streamedTool
	mu         sync.Mutex
}

func newStreamTools(ctx context.Context, cfg Config, emit func(Event) error) *streamTools {
	ctx, cancel := context.WithCancel(ctx)
	dispatcher, _ := NewToolDispatcher(cfg, emit)
	return &streamTools{ctx: ctx, cancel: cancel, dispatcher: dispatcher, pending: make(map[string]*streamedTool)}
}

func (s *streamTools) observe(delta AssistantDelta) error {
	if s == nil || s.dispatcher == nil || delta.Type != "toolcall_complete" || delta.ToolCallID == "" {
		return nil
	}
	for _, call := range delta.Partial.ToolCalls() {
		if call.ID != delta.ToolCallID {
			continue
		}
		t, ok := s.dispatcher.registry.Lookup(call.Name)
		if !ok || !toolapi.PolicyFor(t).ReadOnly || call.ToolType == "custom" {
			return nil
		}
		if call.ArgumentsRaw != "" {
			var args map[string]any
			if json.Unmarshal([]byte(call.ArgumentsRaw), &args) != nil || args == nil {
				return nil
			}
		}
		var copied types.Content
		raw, err := json.Marshal(call)
		if err != nil || json.Unmarshal(raw, &copied) != nil {
			return errors.New("copy completed tool call")
		}
		s.mu.Lock()
		if s.pending[call.ID] != nil {
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		if err := s.dispatcher.emit(Event{Type: ToolCallReady, Timestamp: time.Now().UnixMilli(), ToolCallID: call.ID, ToolName: call.Name, Args: copied.Arguments}); err != nil {
			return err
		}
		prepared := s.dispatcher.prepare(s.ctx, copied, false)
		task := &streamedTool{call: copied, done: make(chan struct{})}
		s.mu.Lock()
		s.pending[call.ID] = task
		s.mu.Unlock()
		go func() { defer close(task.done); task.outcome = s.dispatcher.execute(s.ctx, prepared, nestedOrigin{}) }()
		return nil
	}
	return nil
}

func (s *streamTools) started() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) > 0
}
func (s *streamTools) result(call types.Content) (dispatchedTool, bool) {
	if s == nil {
		return dispatchedTool{}, false
	}
	s.mu.Lock()
	task := s.pending[call.ID]
	s.mu.Unlock()
	if task == nil {
		return dispatchedTool{}, false
	}
	<-task.done
	return task.outcome, true
}
func (s *streamTools) close() {
	if s == nil {
		return
	}
	s.cancel()
	s.mu.Lock()
	tasks := make([]*streamedTool, 0, len(s.pending))
	for _, task := range s.pending {
		tasks = append(tasks, task)
	}
	s.mu.Unlock()
	for _, task := range tasks {
		<-task.done
	}
}
func (s *streamTools) failedAssistant(asst types.Message, emit func(Event) error) error {
	if !s.started() {
		return emitFailedAssistant(asst, emit)
	}
	s.cancel()
	if err := emit(Event{Type: MessageEnd, Message: &asst}); err != nil {
		return err
	}
	for _, call := range asst.ToolCalls() {
		outcome, started := s.result(call)
		var result types.Message
		if started {
			result = outcome.message
		} else {
			result = rejectToolCallsWithReason([]types.Content{call}, "the response failed before this tool was executed", emit)[0]
		}
		if err := emit(Event{Type: MessageStart, Message: &result}); err != nil {
			return err
		}
		if err := emit(Event{Type: MessageEnd, Message: &result}); err != nil {
			return err
		}
	}
	return nil
}
func streamFailedAssistant(s *streamTools, asst types.Message, emit func(Event) error) error {
	if s == nil {
		return emitFailedAssistant(asst, emit)
	}
	return s.failedAssistant(asst, emit)
}
