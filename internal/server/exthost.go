package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ki/internal/compact"
	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/toggles"
)

type extUIState struct {
	Status *extension.UIStatus
	Panel  *extension.UIPanel
	Prompt *extension.UIPrompt
}

type settledEnqueue struct {
	SessionID string
	Extension string
	Req       extension.EnqueueRequest
}

type uiAnswer struct {
	OK    bool
	Value string
}

// CreateSession creates a session for an extension host request.
func (s *Server) CreateSession(req extension.SessionCreateRequest) (extension.SessionCreateResult, error) {
	if strings.TrimSpace(req.WorkspaceID) == "" && strings.TrimSpace(req.CWD) == "" {
		return extension.SessionCreateResult{}, errWorkspaceOrCWDRequired
	}
	rec, err := s.resolveWorkspace(req.WorkspaceID, req.CWD)
	if err != nil {
		return extension.SessionCreateResult{}, err
	}
	ref, model, err := s.registry.ResolveSpec(req.Model, req.Provider)
	if err != nil {
		return extension.SessionCreateResult{}, fmt.Errorf("resolve model: %w", err)
	}
	effort, err := resolveThinking(model, req.ThinkingEffort)
	if err != nil {
		return extension.SessionCreateResult{}, err
	}
	sess, err := session.CreateWithOptions(s.cfg.Sessions.Root, rec.Path, ref.Provider, ref.Model, session.CreateOptions{
		ThinkingEffort: effort,
		Metadata:       req.Metadata,
	})
	if err != nil {
		return extension.SessionCreateResult{}, fmt.Errorf("create session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	if err := s.ws.AttachSession(rec.ID, sess.ID()); err != nil {
		return extension.SessionCreateResult{}, fmt.Errorf("attach session: %w", err)
	}
	s.sidx.Add(sess.ID(), sess.Dir)
	s.rememberModel(ref)
	s.kickWarmup(sess.ID(), sess.Header.CWD)
	return extension.SessionCreateResult{SessionID: sess.ID(), CWD: sess.Header.CWD, WorkspaceID: rec.ID, Metadata: sess.Config.Metadata}, nil
}

// NewSession forks a new session from an existing one's workspace and model.
func (s *Server) NewSession(sessionID string, cwd string) (extension.SessionCreateResult, error) {
	if s.running(sessionID) {
		return extension.SessionCreateResult{}, errSessionBusy
	}
	old, err := s.open(sessionID)
	if err != nil {
		return extension.SessionCreateResult{}, err
	}
	defer func() { _ = old.Close() }()
	workspaceID := ""
	if strings.TrimSpace(cwd) == "" {
		if rec, ok := s.ws.Match(old.Header.CWD); ok {
			workspaceID = rec.ID
		}
		cwd = old.Header.CWD
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(old.Header.CWD, cwd)
	}
	return s.CreateSession(extension.SessionCreateRequest{
		WorkspaceID:    workspaceID,
		CWD:            cwd,
		Provider:       old.Config.Provider,
		Model:          old.Config.Model,
		ThinkingEffort: old.Config.ThinkingEffort,
		Metadata:       old.Config.Metadata,
	})
}

// ReloadSession invalidates one session's resource and extension views.
func (s *Server) ReloadSession(sessionID string) error {
	s.requestReload(sessionID)
	return nil
}

// GetSession returns a snapshot for one session.
func (s *Server) GetSession(sessionID string) (extension.SessionSnapshot, error) {
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return extension.SessionSnapshot{}, errSessionNotFound
	}
	sess, err := session.Open(dir)
	if err != nil {
		return extension.SessionSnapshot{}, fmt.Errorf("open session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	return s.sessionSnapshot(sess), nil
}

// ListSessions returns sessions whose metadata matches filter.
func (s *Server) ListSessions(filter map[string]any) ([]extension.SessionSnapshot, error) {
	infos, err := session.List(s.cfg.Sessions.Root)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]extension.SessionSnapshot, 0, len(infos))
	for _, info := range infos {
		if !matchesMetadata(info.Metadata, filter) {
			continue
		}
		out = append(out, extension.SessionSnapshot{
			ID: info.ID, CWD: info.CWD, Metadata: info.Metadata,
			WorkspaceID: s.workspaceID(info.CWD), Provider: info.Provider, Model: info.Model,
			Idle: !s.running(info.ID), Running: s.running(info.ID),
		})
	}
	return out, nil
}

func (s *Server) sessionSnapshot(sess *session.Session) extension.SessionSnapshot {
	return extension.SessionSnapshot{
		ID: sess.ID(), CWD: sess.Header.CWD, Metadata: sess.Config.Metadata,
		WorkspaceID: s.workspaceID(sess.Header.CWD), Idle: !s.running(sess.ID()),
		Running: s.running(sess.ID()), Provider: sess.Config.Provider, Model: sess.Config.Model, Thinking: sess.Config.ThinkingEffort,
	}
}

func (s *Server) workspaceID(cwd string) string {
	if rec, ok := s.ws.Match(cwd); ok {
		return rec.ID
	}
	return ""
}

func matchesMetadata(metadata, filter map[string]any) bool {
	for key, want := range filter {
		if fmt.Sprint(metadata[key]) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}

// Enqueue starts or queues an extension-originated prompt on a session.
func (s *Server) Enqueue(sessionID, extName string, req extension.EnqueueRequest) (extension.EnqueueResult, error) {
	gate := s.inputGate(sessionID)
	gate.Lock()
	defer gate.Unlock()
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return extension.EnqueueResult{}, errSessionNotFound
	}
	if req.IdempotencyKey != "" {
		s.mu.Lock()
		if prev, ok := s.idempotency[sessionID+"/"+extName+"/"+req.IdempotencyKey]; ok {
			s.mu.Unlock()
			return prev, nil
		}
		s.mu.Unlock()
		sess, err := session.Open(dir)
		if err != nil {
			return extension.EnqueueResult{}, fmt.Errorf("open session: %w", err)
		}
		for _, entry := range sess.Entries() {
			if entry.Type == "message" && entry.IdempotencyKey == req.IdempotencyKey {
				_ = sess.Close()
				return extension.EnqueueResult{Accepted: "duplicate"}, nil
			}
		}
		_ = sess.Close()
		queued, err := session.ReadExtQueue(dir)
		if err != nil {
			return extension.EnqueueResult{}, fmt.Errorf("read ext queue: %w", err)
		}
		for _, item := range queued {
			if item.Extension == extName && item.IdempotencyKey == req.IdempotencyKey {
				return extension.EnqueueResult{Accepted: "queued", QueueID: item.ID}, nil
			}
		}
	}
	contextSequence, err := session.PendingContextSequence(dir)
	if err != nil {
		return extension.EnqueueResult{}, fmt.Errorf("pending context sequence: %w", err)
	}
	req.ContextSequence = contextSequence
	req.ContextBoundary = true
	if req.When == "settled" {
		if s.running(sessionID) {
			s.mu.Lock()
			s.pendingSettled = append(s.pendingSettled, settledEnqueue{SessionID: sessionID, Extension: extName, Req: req})
			s.mu.Unlock()
			return extension.EnqueueResult{Accepted: "scheduled"}, nil
		}
		// Idle settled still lands on the ext FIFO so a waiting user queue wins (E2).
		req.When = "now"
		req.DeliverAs = "queue"
	}
	res, err := s.acceptExtPrompt(sessionID, extName, req)
	if err != nil {
		return extension.EnqueueResult{}, err
	}
	if req.IdempotencyKey != "" {
		s.mu.Lock()
		s.idempotency[sessionID+"/"+extName+"/"+req.IdempotencyKey] = res
		s.mu.Unlock()
	}
	return res, nil
}

func (s *Server) acceptExtPrompt(sessionID, extName string, req extension.EnqueueRequest) (extension.EnqueueResult, error) {
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return extension.EnqueueResult{}, errSessionNotFound
	}
	deliver := req.DeliverAs
	if deliver == "" {
		deliver = toggles.BusyQueue
	}
	item := session.ExtQueuedItem{
		Content:         req.Content,
		Extension:       extName,
		Kind:            req.Kind,
		CustomType:      req.CustomType,
		When:            req.When,
		IdempotencyKey:  req.IdempotencyKey,
		ContextSequence: req.ContextSequence,
		ContextBoundary: req.ContextBoundary,
	}
	item.External = cloneExternal(req.External)
	if deliver == "nextTurn" {
		item.When = "nextTurn"
		got, err := session.EnqueueExt(dir, item)
		if err != nil {
			return extension.EnqueueResult{}, fmt.Errorf("enqueue ext: %w", err)
		}
		s.publishQueueChanged(sessionID)
		return extension.EnqueueResult{Accepted: "queued", QueueID: got.ID}, nil
	}
	if s.running(sessionID) {
		if deliver == toggles.BusySteer {
			if s.pushSteerRun(s.runAt(sessionID), req.Content, req.External) {
				return extension.EnqueueResult{Accepted: "steered"}, nil
			}
		}
		got, err := session.EnqueueExt(dir, item)
		if err != nil {
			return extension.EnqueueResult{}, fmt.Errorf("enqueue ext: %w", err)
		}
		s.publishQueueChanged(sessionID)
		return extension.EnqueueResult{Accepted: "queued", QueueID: got.ID}, nil
	}
	if req.ContextBoundary && req.ContextSequence != 0 {
		if err := s.flushContextMessages(sessionID, req.ContextSequence); err != nil {
			return extension.EnqueueResult{}, err
		}
	}
	st, ctx, err := s.occupy(context.Background(), sessionID)
	if err != nil {
		got, qerr := session.EnqueueExt(dir, item)
		if qerr != nil {
			return extension.EnqueueResult{}, err
		}
		s.publishQueueChanged(sessionID)
		return extension.EnqueueResult{Accepted: "queued", QueueID: got.ID}, nil
	}
	enableRunInbox(st)
	go s.runPrompt(ctx, st, sessionID, req.Content, nil, "", "extension:"+extName, req.IdempotencyKey, nil, req.External)
	return extension.EnqueueResult{Accepted: "started"}, nil
}

// Snapshot returns the live session view for extension host APIs.
func (s *Server) Snapshot(sessionID, _ string) (extension.SessionSnapshot, error) {
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return extension.SessionSnapshot{}, errSessionNotFound
	}
	q, _ := session.ReadQueue(dir)
	eq, _ := session.ReadExtQueue(dir)
	sess, err := session.Open(dir)
	if err != nil {
		return extension.SessionSnapshot{}, fmt.Errorf("open session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	cmds := []string{}
	if s.ext != nil {
		for name := range s.ext.RuntimeCommands(sessionID) {
			cmds = append(cmds, name)
		}
	}
	s.mu.Lock()
	active := append([]string{}, s.activeTools[sessionID]...)
	s.mu.Unlock()
	all := []string{"Read", "Write", "Edit", "Grep", "Glob", "Bash", "TaskOutput", "TaskStop", "Monitor"}
	return extension.SessionSnapshot{
		ID:          sess.ID(),
		CWD:         sess.Header.CWD,
		WorkspaceID: s.workspaceID(sess.Header.CWD),
		Metadata:    sess.Config.Metadata,
		Idle:        !s.running(sessionID),
		Running:     s.running(sessionID),
		Queued:      len(q),
		ExtQueued:   len(eq),
		Provider:    sess.Config.Provider,
		Model:       sess.Config.Model,
		Thinking:    sess.Config.ThinkingEffort,
		ActiveTools: active,
		AllTools:    all,
		Commands:    cmds,
	}, nil
}

func cloneExternal(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func (s *Server) inputGate(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate := s.inputGates[sessionID]
	if gate == nil {
		gate = &sync.Mutex{}
		s.inputGates[sessionID] = gate
	}
	return gate
}

// flushContextMessages commits context-only messages as normal message entries.
// The caller must hold the session input gate so a prompt cannot capture a
// partially flushed transcript.
func (s *Server) flushContextMessages(sessionID string, maxSequence uint64) error {
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return errSessionNotFound
	}
	sess, err := session.Open(dir)
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	if err := session.DrainContextThrough(dir, maxSequence, func(item session.ContextQueuedItem) error {
		_, _, err := sess.AppendMessageWithKey(item.Message, item.IdempotencyKey)
		if err != nil {
			return fmt.Errorf("append message: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("drain context: %w", err)
	}
	return nil
}

// hasPendingOccupy reports queued work that must run before a new context-only
// message is committed. nextTurn items are intentionally excluded because
// they are injected into the next user prompt and do not occupy the session.
func hasPendingOccupy(dir string) (bool, error) {
	userQueue, err := session.ReadQueue(dir)
	if err != nil {
		return false, fmt.Errorf("read queue: %w", err)
	}
	if len(userQueue) > 0 {
		return true, nil
	}
	extQueue, err := session.ReadExtQueue(dir)
	if err != nil {
		return false, fmt.Errorf("read ext queue: %w", err)
	}
	for _, item := range extQueue {
		if item.When != "nextTurn" {
			return true, nil
		}
	}
	return false, nil
}

// AppendMessage records a normal user message without starting a model run.
// When the session is occupied, the message waits in a durable context queue
// and is committed before the next prompt boundary.
func (s *Server) AppendMessage(sessionID, extName string, req extension.AppendMessageRequest) (extension.AppendMessageResult, error) {
	if strings.TrimSpace(extName) == "" {
		return extension.AppendMessageResult{}, errExtensionRequired
	}
	if req.Message.Role == "" {
		req.Message.Role = "user"
	}
	if req.Message.Role != "user" {
		return extension.AppendMessageResult{}, errAppendMessageUserOnly
	}
	if err := validateUserContent(req.Message.Content); err != nil {
		return extension.AppendMessageResult{}, err
	}
	if req.Message.Timestamp == 0 {
		req.Message.Timestamp = time.Now().UnixMilli()
	}
	// The sidecar is not allowed to impersonate another source. Keep the
	// origin canonical even when a request supplies a conflicting value.
	req.Message.Origin = "extension:" + extName

	gate := s.inputGate(sessionID)
	gate.Lock()
	defer gate.Unlock()
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return extension.AppendMessageResult{}, errSessionNotFound
	}
	// Check the transcript before adding a queue item. The queue itself also
	// deduplicates pending items; both checks are needed across a crash where
	// the message entry was committed but the queue cleanup was not.
	if req.IdempotencyKey != "" {
		sess, err := session.Open(dir)
		if err != nil {
			return extension.AppendMessageResult{}, fmt.Errorf("open session: %w", err)
		}
		for _, entry := range sess.Entries() {
			if entry.Type == "message" && entry.IdempotencyKey == req.IdempotencyKey {
				_ = sess.Close()
				return extension.AppendMessageResult{Accepted: "duplicate", EntryID: entry.ID}, nil
			}
		}
		_ = sess.Close()
	}
	item, err := session.EnqueueContext(dir, session.ContextQueuedItem{
		Message:        req.Message,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return extension.AppendMessageResult{}, fmt.Errorf("enqueue context: %w", err)
	}
	if s.running(sessionID) {
		return extension.AppendMessageResult{Accepted: "queued", Sequence: item.Sequence}, nil
	}
	pending, err := hasPendingOccupy(dir)
	if err != nil {
		return extension.AppendMessageResult{}, err
	}
	if pending {
		return extension.AppendMessageResult{Accepted: "queued", Sequence: item.Sequence}, nil
	}
	if err := s.flushContextMessages(sessionID, 0); err != nil {
		return extension.AppendMessageResult{}, err
	}
	return extension.AppendMessageResult{Accepted: "appended", Sequence: item.Sequence}, nil
}

// AppendEntry writes a custom jsonl entry for an extension.
func (s *Server) AppendEntry(sessionID, extName, customType string, data any) error {
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return errSessionNotFound
	}
	if _, err := session.AppendCustomEntry(dir, extName, customType, data); err != nil {
		return fmt.Errorf("append custom entry: %w", err)
	}
	return nil
}

// Abort cancels the live run and any pending UI prompts for the session.
func (s *Server) Abort(sessionID string) error {
	st := s.runAt(sessionID)
	if st != nil {
		st.cancel()
	}
	s.cancelUIPrompts(sessionID)
	return nil
}

func (s *Server) publishNotice(sessionID, extName, tone, text string) {
	s.publishSideband(sessionID, loop.Event{
		Type: loop.ExtensionNotice, Server: extName, Reason: tone, MessageText: text,
	}, false)
}

// Compact runs session compaction under occupy.
func (s *Server) Compact(sessionID string) error {
	sess, err := s.open(sessionID)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	st, ctx, err := s.occupy(context.Background(), sessionID)
	if err != nil {
		return err
	}
	_, err = compact.Run(ctx, sess, s.summarizer(ctx, sess.ID(), sess.Config.Provider, sess.Config.Model), s.cfg.Compaction)
	s.release(sessionID, st)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	s.reloadSession(sessionID)
	return nil
}

// PatchSession updates the session model and/or thinking effort.
func (s *Server) PatchSession(sessionID, model, thinking string) error {
	sess, err := s.open(sessionID)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	if model == "" && thinking == "" {
		return nil
	}
	spec := model
	if spec == "" {
		spec = sess.Config.Model
	}
	ref, selected, err := s.registry.ResolveSpec(spec, sess.Config.Provider)
	if err != nil {
		return fmt.Errorf("resolve model: %w", err)
	}
	effort := sess.Config.ThinkingEffort
	if thinking != "" {
		effort = thinking
	}
	effort, err = resolveThinking(selected, effort)
	if err != nil {
		return err
	}
	if err := sess.SetModelAndThinking(ref.Provider, ref.Model, effort); err != nil {
		return fmt.Errorf("set model: %w", err)
	}
	s.rememberModel(ref)
	return nil
}

// SetActiveTools restricts which tools the next occupy may use.
func (s *Server) SetActiveTools(sessionID, extName string, names []string) error {
	known := map[string]bool{
		"Read": true, "Write": true, "Edit": true, "apply_patch": true,
		"Grep": true, "Glob": true, "Bash": true, "PowerShell": true,
		"TaskOutput": true, "TaskStop": true, "Monitor": true,
	}
	var kept, unknown []string
	for _, n := range names {
		if known[n] {
			kept = append(kept, n)
			continue
		}
		unknown = append(unknown, n)
	}
	s.mu.Lock()
	// Why: do not silently clear the whole set when every name is unknown.
	if len(kept) > 0 || len(names) == 0 {
		s.activeTools[sessionID] = kept
	}
	s.mu.Unlock()
	if len(unknown) > 0 {
		s.publishNotice(sessionID, extName, "warn", "unknown tools: "+strings.Join(unknown, ", "))
	}
	return nil
}

// RegisterTools registers extension-contributed tools on a session.
func (s *Server) RegisterTools(sessionID, extName string, tools []extension.ToolSpec) error {
	if s.ext == nil {
		return errNoExtensions
	}
	if err := s.ext.RegisterTools(sessionID, extName, tools); err != nil {
		return fmt.Errorf("register tools: %w", err)
	}
	return nil
}

// UISetStatus sets or clears a session-scoped top-bar status chip.
func (s *Server) UISetStatus(sessionID, extName, key string, text extension.UIText, tone string) error {
	s.mu.Lock()
	if s.extUI[sessionID] == nil {
		s.extUI[sessionID] = map[string]*extUIState{}
	}
	st := s.extUI[sessionID][extName]
	if st == nil {
		st = &extUIState{}
		s.extUI[sessionID][extName] = st
	}
	if extension.UITextEmpty(text) {
		st.Status = nil
	} else {
		st.Status = &extension.UIStatus{Key: key, Text: text, Tone: tone}
	}
	s.mu.Unlock()
	s.publishExtUI(sessionID)
	return nil
}

// UISetPanel stores a session-scoped extension panel.
func (s *Server) UISetPanel(sessionID, extName string, panel extension.UIPanel) error {
	s.mu.Lock()
	if s.extUI[sessionID] == nil {
		s.extUI[sessionID] = map[string]*extUIState{}
	}
	st := s.extUI[sessionID][extName]
	if st == nil {
		st = &extUIState{}
		s.extUI[sessionID][extName] = st
	}
	p := panel
	st.Panel = &p
	s.mu.Unlock()
	s.publishExtUI(sessionID)
	return nil
}

// UIClearPanel removes a session-scoped extension panel.
func (s *Server) UIClearPanel(sessionID, extName string) error {
	s.mu.Lock()
	if s.extUI[sessionID] != nil && s.extUI[sessionID][extName] != nil {
		s.extUI[sessionID][extName].Panel = nil
	}
	s.mu.Unlock()
	s.publishExtUI(sessionID)
	return nil
}

// GlobalUISetStatus stores the process-level projection emitted by a global
// sidecar during initialize. It is separate from session UI because no
// session may exist when the server first renders the WebUI.
func (s *Server) GlobalUISetStatus(extName, key string, text extension.UIText, tone string) error {
	s.mu.Lock()
	st := s.globalExtUI[extName]
	if st == nil {
		st = &extUIState{}
		s.globalExtUI[extName] = st
	}
	if extension.UITextEmpty(text) {
		st.Status = nil
	} else {
		st.Status = &extension.UIStatus{Key: key, Text: text, Tone: tone}
	}
	s.mu.Unlock()
	return nil
}

// GlobalUISetPanel stores the process-level detail projection. Interactive
// session actions still use session UI; global panels are intentionally a
// read-only entry point until a session is selected.
func (s *Server) GlobalUISetPanel(extName string, panel extension.UIPanel) error {
	s.mu.Lock()
	st := s.globalExtUI[extName]
	if st == nil {
		st = &extUIState{}
		s.globalExtUI[extName] = st
	}
	p := panel
	// Why: global UI has no session-bound action/submit endpoint. Strip
	// interactive controls so the homepage cannot render buttons that do nothing;
	// global configuration remains available through the config editor.
	p.Actions = nil
	p.Fields = nil
	p.SubmitLabel = nil
	st.Panel = &p
	s.mu.Unlock()
	return nil
}

// GlobalUIClearPanel removes the process-level panel for an extension.
func (s *Server) GlobalUIClearPanel(extName string) error {
	s.mu.Lock()
	if st := s.globalExtUI[extName]; st != nil {
		st.Panel = nil
	}
	s.mu.Unlock()
	return nil
}

// UIConfirm prompts the WebUI for a yes/no answer (120s timeout).
func (s *Server) UIConfirm(sessionID, extName string, title, message extension.UIText) (bool, error) {
	ch := make(chan uiAnswer, 1)
	key := sessionID + "/confirm/" + extName
	s.setUIPrompt(sessionID, extName, &extension.UIPrompt{Kind: "confirm", Title: title, Message: message}, key, ch)
	s.publishSideband(sessionID, loop.Event{Type: loop.ExtensionUIPrompt, Server: extName, Reason: "confirm", MessageText: extension.UITextFallback(title)}, false)
	select {
	case a := <-ch:
		s.clearUIPrompt(sessionID, extName, key)
		return a.OK, nil
	case <-time.After(120 * time.Second):
		s.clearUIPrompt(sessionID, extName, key)
		return false, nil
	}
}

// UISelect prompts the WebUI to choose one option (120s timeout).
func (s *Server) UISelect(sessionID, extName string, title extension.UIText, options []string) (string, error) {
	ch := make(chan uiAnswer, 1)
	key := sessionID + "/select/" + extName
	s.setUIPrompt(sessionID, extName, &extension.UIPrompt{Kind: "select", Title: title, Options: options}, key, ch)
	s.publishSideband(sessionID, loop.Event{
		Type: loop.ExtensionUIPrompt, Server: extName, Reason: "select", MessageText: extension.UITextFallback(title), Options: options,
	}, false)
	select {
	case a := <-ch:
		s.clearUIPrompt(sessionID, extName, key)
		return a.Value, nil
	case <-time.After(120 * time.Second):
		s.clearUIPrompt(sessionID, extName, key)
		return "", nil
	}
}

func (s *Server) setUIPrompt(sessionID, extName string, prompt *extension.UIPrompt, key string, ch chan uiAnswer) {
	s.mu.Lock()
	s.uiAnswers[key] = ch
	if s.extUI[sessionID] == nil {
		s.extUI[sessionID] = map[string]*extUIState{}
	}
	st := s.extUI[sessionID][extName]
	if st == nil {
		st = &extUIState{}
		s.extUI[sessionID][extName] = st
	}
	st.Prompt = prompt
	s.mu.Unlock()
	s.publishExtUI(sessionID)
}

func (s *Server) clearUIPrompt(sessionID, extName, key string) {
	s.mu.Lock()
	if key != "" {
		delete(s.uiAnswers, key)
	}
	if s.extUI[sessionID] != nil && s.extUI[sessionID][extName] != nil {
		s.extUI[sessionID][extName].Prompt = nil
	}
	s.mu.Unlock()
	s.publishExtUI(sessionID)
}

func (s *Server) cancelUIPrompts(sessionID string) {
	s.mu.Lock()
	var chans []chan uiAnswer
	for k, ch := range s.uiAnswers {
		if strings.HasPrefix(k, sessionID+"/") {
			delete(s.uiAnswers, k)
			chans = append(chans, ch)
		}
	}
	for _, st := range s.extUI[sessionID] {
		if st != nil {
			st.Prompt = nil
		}
	}
	s.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- uiAnswer{OK: false}:
		default:
		}
	}
	s.publishExtUI(sessionID)
}

// BusEmit sends a bus message and returns the transformed payload.
func (s *Server) BusEmit(sessionID, from, channel string, data any) (any, error) {
	if s.ext == nil {
		return data, nil
	}
	out, err := s.ext.BusEmit(sessionID, from, channel, data)
	if err != nil {
		return out, fmt.Errorf("bus emit: %w", err)
	}
	return out, nil
}

// BusBroadcast fans out a bus message to subscribers without a reply.
func (s *Server) BusBroadcast(sessionID, from, channel string, data any) error {
	if s.ext == nil {
		return nil
	}
	if err := s.ext.BusBroadcast(sessionID, from, channel, data); err != nil {
		return fmt.Errorf("bus broadcast: %w", err)
	}
	return nil
}

func (s *Server) extensionUIList(sessionID string) []extension.UI {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []extension.UI
	for name, st := range s.extUI[sessionID] {
		if st == nil {
			continue
		}
		out = append(out, cloneExtensionUI(name, st))
	}
	return out
}

func (s *Server) globalExtensionUI(name string) *extension.UI {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.globalExtUI[name]
	if st == nil {
		return nil
	}
	ui := cloneExtensionUI(name, st)
	if ui.Status == nil && ui.Panel == nil && ui.Prompt == nil {
		return nil
	}
	return &ui
}

func cloneExtensionUI(name string, st *extUIState) extension.UI {
	ui := extension.UI{Extension: name}
	if st.Status != nil {
		status := *st.Status
		ui.Status = &status
	}
	if st.Panel != nil {
		panel := *st.Panel
		panel.Sections = append([]map[string]any(nil), st.Panel.Sections...)
		panel.Actions = append([]extension.UIAction(nil), st.Panel.Actions...)
		panel.Fields = append([]extension.UIField(nil), st.Panel.Fields...)
		for i := range panel.Fields {
			panel.Fields[i].Options = append([]string(nil), st.Panel.Fields[i].Options...)
		}
		ui.Panel = &panel
	}
	if st.Prompt != nil {
		prompt := *st.Prompt
		prompt.Options = append([]string(nil), st.Prompt.Options...)
		ui.Prompt = &prompt
	}
	return ui
}

func (s *Server) publishExtUI(sessionID string) {
	s.publishSideband(sessionID, loop.Event{Type: "extension_ui_updated"}, false)
}

func (s *Server) flushSettled(sessionID string) {
	gate := s.inputGate(sessionID)
	gate.Lock()
	defer gate.Unlock()
	s.mu.Lock()
	var rest []settledEnqueue
	var due []settledEnqueue
	for _, p := range s.pendingSettled {
		if p.SessionID == sessionID {
			due = append(due, p)
		} else {
			rest = append(rest, p)
		}
	}
	s.pendingSettled = rest
	s.mu.Unlock()
	if len(due) == 0 {
		return
	}
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return
	}
	// Land on the ext FIFO only. release then dispatchQueue so a waiting user
	// queue occupies first (E2). Never occupy from settled flush.
	for _, p := range due {
		_, _ = session.EnqueueExt(dir, session.ExtQueuedItem{
			Content:         p.Req.Content,
			Extension:       p.Extension,
			Kind:            p.Req.Kind,
			CustomType:      p.Req.CustomType,
			IdempotencyKey:  p.Req.IdempotencyKey,
			ContextSequence: p.Req.ContextSequence,
			ContextBoundary: p.Req.ContextBoundary,
			External:        cloneExternal(p.Req.External),
		})
	}
	s.publishQueueChanged(sessionID)
}

func (s *Server) extensionUIAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind      string         `json:"kind"`
		Extension string         `json:"extension"`
		OK        bool           `json:"ok"`
		Value     string         `json:"value"`
		Fields    map[string]any `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	switch body.Kind {
	case "action":
		if s.ext != nil {
			s.ext.Notify(id, body.Extension, "ui.action", map[string]any{"id": body.Value})
		}
	case "submit":
		if s.ext != nil {
			s.ext.Notify(id, body.Extension, "ui.submit", map[string]any{"fields": body.Fields})
		}
	default:
		s.answerUI(id, body.Kind, body.Extension, body.OK, body.Value)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) answerUI(sessionID, kind, extName string, ok bool, value string) {
	key := sessionID + "/" + kind + "/" + extName
	s.mu.Lock()
	ch := s.uiAnswers[key]
	delete(s.uiAnswers, key)
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- uiAnswer{OK: ok, Value: value}:
		default:
		}
	}
}
