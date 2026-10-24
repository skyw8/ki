package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"ki/internal/compact"
	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/types"
)

type runEmitter struct {
	mu            sync.Mutex
	s             *Server
	ctx           context.Context
	id            string
	sess          *session.Session
	st            *runState
	info          provider.Model
	serverSide    bool
	binding       types.ProviderBinding
	replayBinding types.ProviderBinding

	idempotencyKey     string
	cachePrevPrompt    int
	cacheReported      bool
	cacheBaselineReady bool
}

func (p *runEmitter) Emit(ev loop.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.persist(&ev); err != nil {
		return err
	}
	p.buffer(&ev)
	if ev.Type == loop.MessageEnd && ev.Message != nil && ev.Message.QuestionReply != nil {
		if q, ok := p.sess.Question(ev.Message.QuestionReply.QuestionItemID); ok {
			p.s.publishQuestion(p.id, q)
		}
	}
	if p.st.agentTaskID != "" {
		if err := p.s.agentTasks.ReduceProgress(p.st.agentTaskID, p.st.agentGeneration, projectAgentProgress(ev)); err != nil {
			return fmt.Errorf("persist agent progress: %w", err)
		}
	}
	if ev.Type == loop.AgentEnd {
		p.publishCompletion(ev)
	}
	p.notifyExtensions(ev)
	if err := p.recordContextUsage(ev); err != nil {
		return err
	}
	return nil
}

func (p *runEmitter) persist(ev *loop.Event) error {
	if ev.ParentCallID != "" && (ev.Type == loop.ToolExecutionStart || ev.Type == loop.ToolExecutionUpdate || ev.Type == loop.ToolExecutionEnd) {
		bounded, err := loop.BoundNestedToolEvent(*ev)
		if err != nil {
			return err
		}
		*ev = bounded
		details := map[string]any{
			"toolCallId": ev.ToolCallID, "parentCallId": ev.ParentCallID, "cellId": ev.CellID,
			"toolName": ev.ToolName, "requestedToolName": ev.RequestedToolName,
			"timestamp": ev.Timestamp, "durationMs": ev.DurationMs, "isError": ev.IsError,
			"args": ev.Args, "partialResult": ev.PartialResult, "result": ev.Result,
		}
		entry, err := p.sess.AppendDetailsEvent(string(ev.Type), details)
		if err != nil {
			return fmt.Errorf("append nested tool audit: %w", err)
		}
		ev.EntryID, ev.ParentID = entry.ID, &entry.ParentID
		return nil
	}
	switch ev.Type {
	case loop.MessageEnd:
		if ev.Message == nil {
			return nil
		}
		return p.appendMessage(ev)
	case loop.RequestHeader:
		return p.appendRequestHeader(*ev)
	case loop.CompactionStart, loop.CompactionEnd:
		if err := persistCompactionEvent(p.sess, ev); err != nil {
			return fmt.Errorf("append loop event: %w", err)
		}
		if ev.Type == loop.CompactionEnd && ev.Status == "committed" {
			p.cachePrevPrompt, p.cacheReported, p.cacheBaselineReady = 0, false, true
		}
	case loop.ToolExecutionUpdate:
		progress, err := json.Marshal(ev.PartialResult)
		if err != nil {
			return fmt.Errorf("marshal tool progress: %w", err)
		}
		if _, err := p.sess.AppendEvent(string(ev.Type), string(progress), true); err != nil {
			return fmt.Errorf("append tool progress: %w", err)
		}
	case loop.PatchApplyUpdated:
		details := map[string]any{"toolCallId": ev.ToolCallID, "toolName": ev.ToolName, "partialResult": ev.PartialResult}
		if _, err := p.sess.AppendDetailsEvent(string(ev.Type), details); err != nil {
			return fmt.Errorf("append patch preview: %w", err)
		}
	}
	return nil
}

func persistCompactionEvent(sess *session.Session, ev *loop.Event) error {
	details := map[string]any{
		"reason": ev.Reason, "ok": ev.OK, "willRetry": ev.WillRetry,
		"strategy": ev.Strategy, "status": ev.Status,
		"fromExtension": ev.FromExtension,
		"entryId":       ev.EntryID, "firstKeptEntryId": ev.FirstKeptEntryID,
		"tokensBefore": ev.TokensBefore, "usage": ev.Usage,
	}
	entry, err := sess.AppendDetailsEvent(string(ev.Type), details)
	if err != nil {
		return err
	}
	stamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return fmt.Errorf("parse compaction entry timestamp: %w", err)
	}
	ev.LifecycleEntryID = entry.ID
	ev.ParentID = &entry.ParentID
	ev.Timestamp = stamp.UnixMilli()
	return nil
}

func (p *runEmitter) appendMessage(ev *loop.Event) error {
	responsesItems := slices.Clone(ev.Message.ResponsesItems)
	if p.s.ext != nil && ev.Message.QuestionReply == nil && !isQuestionSide(p.sess.Config) {
		rewritten := p.s.ext.ApplyMessageEnd(p.ctx, p.id, *ev.Message)
		rewritten.ResponsesItems = responsesItems
		preserveMessageIdentity(*ev.Message, &rewritten)
		ev.Message = &rewritten
	}
	if ev.Message.StopReason == "aborted" {
		reason, source := runCancellation(p.st)
		if reason != "" {
			annotated := *ev.Message
			annotated.CancelReason = reason
			annotated.CancelSource = source
			ev.Message = &annotated
		}
	}
	ev.CacheMiss = nil
	if ev.Message.Role == "assistant" && ev.Message.Usage != nil {
		if !p.cacheBaselineReady {
			entries := p.sess.LeafEntries()
			leafID := ""
			if len(entries) > 0 {
				leafID = entries[len(entries)-1].ID
			}
			p.cachePrevPrompt, p.cacheReported = session.PromptCacheBaseline(entries, leafID)
			p.cacheBaselineReady = true
		}
		ev.CacheMiss = session.ClassifyCacheMiss(p.cachePrevPrompt, p.cacheReported, ev.Message.Usage)
		prompt := ev.Message.Usage.Input + ev.Message.Usage.CacheRead + ev.Message.Usage.CacheWrite
		if prompt > 0 {
			p.cachePrevPrompt = prompt
			p.cacheReported = p.cacheReported || ev.Message.Usage.CacheRead+ev.Message.Usage.CacheWrite > 0
		}
	}
	key := ""
	if ev.Message.ContextOnly || ev.Message.QuestionReply != nil {
		key = ev.Message.ClientRequestID
	}
	if ev.Message.Role == "user" && p.idempotencyKey != "" {
		key = p.idempotencyKey
		p.idempotencyKey = ""
	}
	persisted := *ev.Message
	persisted.ResponsesItems = nil
	if len(ev.Message.ResponsesItems) > 0 {
		if !p.serverSide {
			ev.Message = &persisted
		} else if !p.replayBinding.SameScope(p.binding) ||
			(ev.Message.Provider != "" && ev.Message.Provider != p.info.Provider) ||
			(ev.Message.Model != "" && ev.Message.Model != p.info.ID) {
			ev.Message = &persisted
		} else {
			items, checkpointErr := provider.ResponsesCheckpointItems(*ev.Message)
			if checkpointErr != nil {
				return fmt.Errorf("build server compaction checkpoint: %w", checkpointErr)
			}
			checkpoint := types.ResponsesContext{Binding: p.binding, Items: items}
			messages := append(p.sess.MessagesToLeaf(), persisted)
			tokensBefore := compact.EstimateTokens(messages, p.sess.LastCompactionAt())
			messageEntry, _, checkpointErr := p.sess.AppendMessageAndResponsesCompaction(persisted, checkpoint, tokensBefore)
			if checkpointErr != nil {
				return fmt.Errorf("append server compaction checkpoint: %w", checkpointErr)
			}
			ev.EntryID, ev.ParentID = messageEntry.ID, &messageEntry.ParentID
			p.cachePrevPrompt, p.cacheReported, p.cacheBaselineReady = 0, false, true
			ev.Message = &persisted
			return nil
		}
	}
	e, _, err := p.sess.AppendMessageWithKey(persisted, key)
	if err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	ev.EntryID, ev.ParentID = e.ID, &e.ParentID
	return nil
}

func preserveMessageIdentity(original types.Message, rewritten *types.Message) {
	rewritten.QuestionReply = original.QuestionReply
	rewritten.ClientRequestID = original.ClientRequestID
	rewritten.Completion = original.Completion
	rewritten.Origin = original.Origin
}

func (p *runEmitter) appendRequestHeader(ev loop.Event) error {
	tools := make([]session.ToolSchema, 0, len(ev.Tools))
	for _, t := range ev.Tools {
		var format *session.ToolFormat
		if t.Format != nil {
			format = &session.ToolFormat{Type: t.Format.Type, Syntax: t.Format.Syntax, Definition: t.Format.Definition}
		}
		tools = append(tools, session.ToolSchema{Type: t.Type, Name: t.Name, Description: t.Description, Parameters: t.Parameters, Format: format})
	}
	meta := session.RequestMeta{
		Provider:       p.sess.Config.Provider,
		Model:          p.sess.Config.Model,
		ThinkingEffort: p.sess.Config.ThinkingEffort,
		CatalogVersion: provider.CatalogVersion,
		Pricing:        p.info.Cost,
	}
	if _, err := p.sess.AppendRequestHeader(ev.System, tools, meta); err != nil {
		return fmt.Errorf("append request header: %w", err)
	}
	return nil
}

func (p *runEmitter) buffer(ev *loop.Event) {
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	p.st.appendLocked(ev)
	p.st.wait.Broadcast()
}

func (p *runEmitter) publishCompletion(ev loop.Event) {
	p.s.publishPush(p.id, loop.Event{Type: ev.Type, RunID: ev.RunID, External: ev.External})
	p.s.notifyPushCompletion(p.id)
}

func (p *runEmitter) notifyExtensions(ev loop.Event) {
	if isQuestionSide(p.sess.Config) {
		return
	}
	if !ev.Type.Lifecycle() {
		return
	}
	p.s.ext.OnEvent(p.ctx, p.id, extension.RedactEvent(ev, p.id))
}

func (p *runEmitter) recordContextUsage(ev loop.Event) error {
	live := ev.Type == loop.RequestHeader
	if !live && (ev.Type != loop.MessageEnd || ev.Message == nil || ev.Message.Role != "assistant") {
		return nil
	}
	var request *loop.Event
	if live {
		request = &ev
	}
	used, window, err := p.s.contextUsageEstimate(p.sess, p.info, p.replayBinding, request)
	if err != nil {
		return err
	}
	estimated := live || !usableUsage(ev.Message.Usage)
	if _, err := p.sess.AppendContextUsage(used, window, estimated); err != nil {
		return fmt.Errorf("append context usage: %w", err)
	}
	usage := loop.Event{
		Type:           loop.ContextUsage,
		Provider:       p.sess.Config.Provider,
		Model:          p.sess.Config.Model,
		CatalogVersion: provider.CatalogVersion,
		UsedTokens:     used,
		ContextWindow:  window,
		Estimated:      estimated,
	}
	p.buffer(&usage)
	return nil
}

func (p *runEmitter) compactNow(intent compact.Intent, request *loop.Request) (bool, error) {
	reason := string(intent.Reason)
	if err := p.Emit(loop.Event{
		Type: loop.CompactionStart, Reason: reason, WillRetry: intent.WillRetry,
	}); err != nil {
		return false, fmt.Errorf("persist compaction start: %w", err)
	}
	outcome, err := p.s.compactSession(p.ctx, p.sess, intent, request)
	if errors.Is(err, compact.ErrCompactionSkipped) {
		if emitErr := p.Emit(loop.Event{
			Type: loop.CompactionEnd, Reason: reason, WillRetry: intent.WillRetry,
			Status: "cancelled",
		}); emitErr != nil {
			return false, errors.Join(err, fmt.Errorf("persist compaction end: %w", emitErr))
		}
		return false, err
	}
	if err != nil && !errors.Is(err, compact.ErrNothingToCompact) {
		slog.Warn(reason+" compact", "session_id", p.id, "err", err)
		if emitErr := p.Emit(loop.Event{
			Type: loop.CompactionEnd, Reason: reason, WillRetry: intent.WillRetry,
			Status: "failed",
		}); emitErr != nil {
			return false, errors.Join(err, fmt.Errorf("persist compaction end: %w", emitErr))
		}
		return false, err
	}
	status := "committed"
	if errors.Is(err, compact.ErrNothingToCompact) {
		status = "empty"
	}
	if emitErr := p.Emit(loop.Event{
		Type: loop.CompactionEnd, Reason: reason, OK: true,
		WillRetry: intent.WillRetry, Status: status,
		EntryID: outcome.Entry.ID, Strategy: outcome.Strategy,
		FromExtension:    outcome.FromExtension,
		FirstKeptEntryID: outcome.FirstKeptEntryID,
		TokensBefore:     outcome.TokensBefore, Usage: outcome.Usage,
	}); emitErr != nil {
		return err == nil, fmt.Errorf("persist compaction end: %w", emitErr)
	}
	return err == nil, err
}

func (s *Server) contextUsageEstimate(sess *session.Session, info provider.Model, binding types.ProviderBinding, request *loop.Event) (used, window int, err error) {
	modelContext := sess.ContextToLeaf(binding)
	messages := modelContext.Messages
	last := sess.LastCompactionAt()
	used = compact.EstimateModelContext(modelContext, last)
	window = s.effectiveContextWindow(info)
	if hasUsableContextUsage(messages, last) {
		return used, window, nil
	}
	system := ""
	var tools any
	if request != nil {
		system, tools = request.System, request.Tools
	} else {
		persistedSystem, persistedTools, ok := sess.LastRequestHeader()
		if !ok {
			return used, window, nil
		}
		system, tools = persistedSystem, persistedTools
	}
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		return used, window, fmt.Errorf("marshal tool schemas: %w", err)
	}
	return used + (len(system)+len(toolJSON)+3)/4, window, nil
}
