package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"ki/internal/compact"
	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
)

// runEmitter is one run's event funnel. loop.Run produces events and calls Emit
// for each of them; the funnel applies an event to its subscribers in a fixed
// order:
//
//  1. persist    — the jsonl entry, when the event has one
//  2. buffer     — this run's SSE replay log in runState
//  3. push       — the WebUI push stream (terminal frames only)
//  4. extensions — async lifecycle notifications
//
// plus the context meter (persisted and buffered) and the run's threshold
// compaction.
//
// Why a type instead of the closure runPrompt used to build: the stages are
// independent contracts (jsonl shape, SSE replay, push fallback, extension
// ordering, context accounting) that were only reachable through a full run.
// The order between them is still a contract, so Emit stays one synchronous
// call chain on the loop's goroutine — no stage may spawn a goroutine, see
// notifyExtensions.
type runEmitter struct {
	s    *Server
	ctx  context.Context
	id   string
	sess *session.Session
	st   *runState
	// info is the resolved catalog entry for the session's provider/model. It
	// supplies the context window and the pricing pinned into the request
	// header.
	info provider.Model

	// idempotencyKey is consumed by the first user message_end, so a retried
	// extension enqueue appends one entry instead of two. Empty when the run
	// carries no key.
	idempotencyKey string
}

// Emit applies one loop event to every subscriber. An error aborts the run:
// the loop stops on the first event the server could not persist.
func (p *runEmitter) Emit(ev loop.Event) error {
	if err := p.persist(&ev); err != nil {
		return err
	}
	p.buffer(ev)
	if ev.Type == loop.AgentEnd {
		p.publishCompletion(ev)
	}
	p.notifyExtensions(ev)
	if err := p.recordContextUsage(ev); err != nil {
		return err
	}
	if ev.Type == loop.AgentEnd {
		// Threshold compaction runs last: it re-enters Emit with its own
		// compaction_start/end events, which must land in loop order behind the
		// agent_end they follow.
		p.autoCompact()
	}
	return nil
}

// persist writes the event's jsonl entry before the event is buffered, so a
// client that sees an event can always find it on disk. It may rewrite ev: an
// extension message_end rewrite and the appended entry id must reach the SSE
// subscriber, which is why it takes a pointer.
func (p *runEmitter) persist(ev *loop.Event) error {
	switch ev.Type {
	case loop.MessageEnd:
		if ev.Message == nil {
			return nil
		}
		return p.appendMessage(ev)
	case loop.RequestHeader:
		return p.appendRequestHeader(*ev)
	case loop.CompactionStart, loop.CompactionEnd:
		// Compaction progress is persisted too (decision: jsonl + SSE) so a
		// session replay shows when compaction happened.
		if _, err := p.sess.AppendEvent(string(ev.Type), ev.Reason, ev.OK); err != nil {
			return fmt.Errorf("append loop event: %w", err)
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

// appendMessage persists a message_end. Message rewriting stays ahead of the
// append and of the SSE replay of the same event.
func (p *runEmitter) appendMessage(ev *loop.Event) error {
	if p.s.ext != nil {
		rewritten := p.s.ext.ApplyMessageEnd(p.ctx, p.id, *ev.Message)
		ev.Message = &rewritten
	}
	key := ""
	if ev.Message.Role == "user" && p.idempotencyKey != "" {
		key = p.idempotencyKey
		p.idempotencyKey = ""
	}
	e, _, err := p.sess.AppendMessageWithKey(*ev.Message, key)
	if err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	ev.EntryID = e.ID
	return nil
}

// appendRequestHeader persists the model-facing system prompt and tool schemas
// of one provider request.
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

// buffer appends the event to this run's replay log and wakes its SSE readers.
// Every buffered event carries the run id and the run's external metadata, so a
// reader that starts mid-run can still attribute what it replays.
func (p *runEmitter) buffer(ev loop.Event) {
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	ev.RunID = p.st.runID
	ev.External = cloneExternal(p.st.external)
	p.st.evs = append(p.st.evs, ev)
	p.st.wait.Broadcast()
}

// publishCompletion fans a terminal event out to push subscribers: a WebUI tab
// that is not holding this run's SSE (background session, or the user switched
// to another session) still needs to learn the run finished. run_aborted
// already fans out through publishSideband.
//
// Publish without Messages: the run SSE replays them for the one client that
// asked, while every tab needs only the completion. The full event still
// reaches extensions.
func (p *runEmitter) publishCompletion(ev loop.Event) {
	p.s.publishPush(p.id, loop.Event{Type: ev.Type, RunID: ev.RunID, External: ev.External})
}

// notifyExtensions forwards a lifecycle notification to async subscribers.
//
// Lifecycle notifications are asynchronous from the extension's point of view,
// but their write order must match the loop. Spawning one goroutine per event
// allowed agent_settled to overtake message_end, leaving channel connectors with
// an ephemeral draft but no final reply.
func (p *runEmitter) notifyExtensions(ev loop.Event) {
	if !ev.Type.Lifecycle() {
		return
	}
	p.s.ext.OnEvent(p.ctx, p.id, extension.RedactEvent(ev, p.id))
}

// recordContextUsage persists and buffers the context meter after a
// request_header and after an assistant message_end: the only two points where
// the model-facing size can change. Other events are not persisted here.
func (p *runEmitter) recordContextUsage(ev loop.Event) error {
	live := ev.Type == loop.RequestHeader
	if !live && (ev.Type != loop.MessageEnd || ev.Message == nil || ev.Message.Role != "assistant") {
		return nil
	}
	var request *loop.Event
	if live {
		// The live request supplies the system prompt and tool schemas that the
		// message estimate does not cover.
		request = &ev
	}
	used, window, err := p.s.contextUsageEstimate(p.sess, p.info, request)
	if err != nil {
		return err
	}
	estimated := live || !usableUsage(ev.Message.Usage)
	if _, err := p.sess.AppendContextUsage(used, window, estimated); err != nil {
		return fmt.Errorf("append context usage: %w", err)
	}
	p.buffer(loop.Event{
		Type:           loop.ContextUsage,
		Provider:       p.sess.Config.Provider,
		Model:          p.sess.Config.Model,
		CatalogVersion: provider.CatalogVersion,
		UsedTokens:     used,
		ContextWindow:  window,
		Estimated:      estimated,
	})
	return nil
}

// autoCompact applies the threshold check after agent_end and compacts an
// oversized context so the next prompt starts fresh.
func (p *runEmitter) autoCompact() {
	if !p.s.shouldCompact(p.sess, p.info.ContextWindow) {
		return
	}
	if p.compactNow("threshold") {
		// The run SSE stops at agent_end, so the rebuilt context reaches the
		// meter through the push stream.
		p.s.publishContextUsage(p.sess)
	}
}

// compactNow runs one compaction and reports whether the context actually
// changed. The compaction events go through Emit like every other event, so
// they are persisted, buffered, and fanned out in loop order. A compaction that
// finds nothing to do is not an error.
func (p *runEmitter) compactNow(reason string) bool {
	_ = p.Emit(loop.Event{Type: loop.CompactionStart, Reason: reason})
	_, err := p.s.compactSession(p.ctx, p.sess)
	if err != nil && !errors.Is(err, compact.ErrNothingToCompact) {
		slog.Warn(reason+" compact", "session_id", p.id, "err", err)
		_ = p.Emit(loop.Event{Type: loop.CompactionEnd, Reason: reason, OK: false})
		return false
	}
	_ = p.Emit(loop.Event{Type: loop.CompactionEnd, Reason: reason, OK: true})
	return err == nil
}

// contextUsageEstimate returns the model-facing token estimate and the effective
// context window for sess.
//
// The message estimate is char/4 whenever the newest assistant usage is missing
// or predates the last compaction, and it never covers the system prompt and
// tool schemas. Those are added from request when the caller serves that request
// (request_header), otherwise from the session's persisted last request header —
// an assistant message_end without usable usage, or a compaction that ran
// outside a run, has no live request left. The returned estimate is still useful
// when marshaling the live schemas fails; the error is reported so the
// request_header path can abort the run.
func (s *Server) contextUsageEstimate(sess *session.Session, info provider.Model, request *loop.Event) (used, window int, err error) {
	messages := sess.MessagesToLeaf()
	last := sess.LastCompactionAt()
	used = compact.EstimateTokens(messages, last)
	window = info.ContextWindow
	if maxContext := s.cfg.Compaction.MaxContextTokens; maxContext > 0 {
		window = min(window, maxContext)
	}
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
