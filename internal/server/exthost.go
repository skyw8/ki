package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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

func (s *Server) Enqueue(sessionID, extName string, req extension.EnqueueRequest) (extension.EnqueueResult, error) {
	if req.IdempotencyKey != "" {
		s.mu.Lock()
		if prev, ok := s.idempotency[sessionID+"/"+extName+"/"+req.IdempotencyKey]; ok {
			s.mu.Unlock()
			return prev, nil
		}
		s.mu.Unlock()
	}
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
		return extension.EnqueueResult{}, fmt.Errorf("session not found")
	}
	deliver := req.DeliverAs
	if deliver == "" {
		deliver = toggles.BusyQueue
	}
	item := session.ExtQueuedItem{Content: req.Content, Extension: extName, Kind: req.Kind, CustomType: req.CustomType}
	if deliver == "nextTurn" {
		item.When = "nextTurn"
		got, err := session.EnqueueExt(dir, item)
		if err != nil {
			return extension.EnqueueResult{}, err
		}
		s.publishQueueChanged(sessionID)
		return extension.EnqueueResult{Accepted: "queued", QueueID: got.ID}, nil
	}
	if s.running(sessionID) {
		if deliver == toggles.BusySteer {
			if s.pushSteerRun(s.runAt(sessionID), req.Content) {
				return extension.EnqueueResult{Accepted: "steered"}, nil
			}
		}
		got, err := session.EnqueueExt(dir, item)
		if err != nil {
			return extension.EnqueueResult{}, err
		}
		s.publishQueueChanged(sessionID)
		return extension.EnqueueResult{Accepted: "queued", QueueID: got.ID}, nil
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
	go s.runPrompt(ctx, st, sessionID, req.Content, nil, "", "extension:"+extName, nil)
	return extension.EnqueueResult{Accepted: "started"}, nil
}

func (s *Server) Snapshot(sessionID, _ string) (extension.SessionSnapshot, error) {
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return extension.SessionSnapshot{}, fmt.Errorf("session not found")
	}
	q, _ := session.ReadQueue(dir)
	eq, _ := session.ReadExtQueue(dir)
	sess, err := session.Open(dir)
	if err != nil {
		return extension.SessionSnapshot{}, err
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
		Idle:        !s.running(sessionID),
		Running:     s.running(sessionID),
		Queued:      len(q),
		ExtQueued:   len(eq),
		Model:       sess.Config.Model,
		Thinking:    sess.Config.ThinkingEffort,
		ActiveTools: active,
		AllTools:    all,
		Commands:    cmds,
	}, nil
}

func (s *Server) AppendEntry(sessionID, extName, customType string, data any) error {
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return fmt.Errorf("session not found")
	}
	_, err := session.AppendCustomEntry(dir, extName, customType, data)
	return err
}

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

func (s *Server) Compact(sessionID string) error {
	sess, err := s.open(sessionID)
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	_, err = s.compactSession(context.Background(), sess)
	return err
}

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
		return err
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
		return err
	}
	s.rememberModel(ref)
	return nil
}

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
	if len(kept) == 0 && len(names) > 0 {
		// Do not silently clear the whole set when every name is unknown.
		kept = append([]string{}, s.activeTools[sessionID]...)
	} else {
		s.activeTools[sessionID] = kept
	}
	s.mu.Unlock()
	if len(unknown) > 0 {
		s.publishNotice(sessionID, extName, "warn", "unknown tools: "+strings.Join(unknown, ", "))
	}
	return nil
}

func (s *Server) RegisterTools(sessionID, extName string, tools []extension.ToolSpec) error {
	if s.ext == nil {
		return fmt.Errorf("no extensions")
	}
	return s.ext.RegisterTools(sessionID, extName, tools)
}

func (s *Server) UISetStatus(sessionID, extName, key, text, tone string) error {
	s.mu.Lock()
	if s.extUI[sessionID] == nil {
		s.extUI[sessionID] = map[string]*extUIState{}
	}
	st := s.extUI[sessionID][extName]
	if st == nil {
		st = &extUIState{}
		s.extUI[sessionID][extName] = st
	}
	if text == "" {
		st.Status = nil
	} else {
		st.Status = &extension.UIStatus{Key: key, Text: text, Tone: tone}
	}
	s.mu.Unlock()
	s.publishExtUI(sessionID)
	return nil
}

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

func (s *Server) UIClearPanel(sessionID, extName string) error {
	s.mu.Lock()
	if s.extUI[sessionID] != nil && s.extUI[sessionID][extName] != nil {
		s.extUI[sessionID][extName].Panel = nil
	}
	s.mu.Unlock()
	s.publishExtUI(sessionID)
	return nil
}

func (s *Server) UIConfirm(sessionID, extName, title, message string) (bool, error) {
	ch := make(chan uiAnswer, 1)
	key := sessionID + "/confirm/" + extName
	s.setUIPrompt(sessionID, extName, &extension.UIPrompt{Kind: "confirm", Title: title, Message: message}, key, ch)
	s.publishSideband(sessionID, loop.Event{Type: loop.ExtensionUIPrompt, Server: extName, Reason: "confirm", MessageText: title}, false)
	select {
	case a := <-ch:
		s.clearUIPrompt(sessionID, extName, key)
		return a.OK, nil
	case <-time.After(120 * time.Second):
		s.clearUIPrompt(sessionID, extName, key)
		return false, nil
	}
}

func (s *Server) UISelect(sessionID, extName, title string, options []string) (string, error) {
	ch := make(chan uiAnswer, 1)
	key := sessionID + "/select/" + extName
	s.setUIPrompt(sessionID, extName, &extension.UIPrompt{Kind: "select", Title: title, Options: options}, key, ch)
	s.publishSideband(sessionID, loop.Event{
		Type: loop.ExtensionUIPrompt, Server: extName, Reason: "select", MessageText: title, Options: options,
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

func (s *Server) BusEmit(sessionID, from, channel string, data any) (any, error) {
	if s.ext == nil {
		return data, nil
	}
	return s.ext.BusEmit(sessionID, from, channel, data)
}

func (s *Server) BusBroadcast(sessionID, from, channel string, data any) error {
	if s.ext == nil {
		return nil
	}
	return s.ext.BusBroadcast(sessionID, from, channel, data)
}

func (s *Server) extensionUIList(sessionID string) []extension.ExtensionUI {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []extension.ExtensionUI
	for name, st := range s.extUI[sessionID] {
		if st == nil {
			continue
		}
		out = append(out, extension.ExtensionUI{Extension: name, Status: st.Status, Panel: st.Panel, Prompt: st.Prompt})
	}
	return out
}

func (s *Server) publishExtUI(sessionID string) {
	s.publishSideband(sessionID, loop.Event{Type: "extension_ui_updated"}, false)
}

func (s *Server) flushSettled(sessionID string) {
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
			Content: p.Req.Content, Extension: p.Extension, Kind: p.Req.Kind, CustomType: p.Req.CustomType,
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
