package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/mcp"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/toggles"
	"ki/internal/types"
)

var errSessionBusy = errors.New("session busy")

func (s *Server) workspacePath(id string) string {
	if id == "" {
		return ""
	}
	rec, ok := s.ws.Get(id)
	if !ok {
		return ""
	}
	return rec.Path
}

func (s *Server) doReload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	var queued bool
	if body.SessionID == "" {
		queued = s.Reload()
	} else {
		queued = s.requestReload(body.SessionID)
	}
	slog.Info("reload")
	writeJSON(w, 200, map[string]any{"ok": true, "queued": queued})
}

func (s *Server) getSkills(w http.ResponseWriter, r *http.Request) {
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	tg := toggles.Load(s.cfg.Home)
	snapshot := s.resources.Scan(cwd)
	items := []map[string]any{}
	for _, item := range snapshot.Skills {
		items = append(items, map[string]any{
			"name":        item.Name,
			"description": item.Description,
			"path":        item.FilePath,
			"source":      item.Source,
			"enabled":     tg.Skills.Allowed(item.Name),
		})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) patchSkills(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	f := toggles.Load(s.cfg.Home)
	f.Skills = session.Toggle{Disabled: body.Disabled}
	if err := toggles.Save(s.cfg.Home, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Reload()
	s.getSkills(w, r)
}

func (s *Server) getMCP(w http.ResponseWriter, r *http.Request) {
	tg := toggles.Load(s.cfg.Home)
	file := s.resources.Scan("").MCP
	items := []map[string]any{}
	for _, item := range mcp.List(file, tg.MCP) {
		row := map[string]any{
			"name":    item.Name,
			"command": item.Command,
			"source":  item.Source,
			"enabled": item.Enabled,
			"status":  mcp.StatusUnloaded,
		}
		if len(item.Args) > 0 {
			row["args"] = item.Args
		}
		if item.URL != "" {
			row["url"] = item.URL
		}
		items = append(items, row)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) patchMCP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	f := toggles.Load(s.cfg.Home)
	f.MCP = session.Toggle{Disabled: body.Disabled}
	if err := toggles.Save(s.cfg.Home, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Reload()
	s.getMCP(w, r)
}

func (s *Server) getExtensions(w http.ResponseWriter, r *http.Request) {
	snapshot := s.resources.Scan("")
	writeJSON(w, 200, map[string]any{"items": s.extensionCatalog(snapshot)})
}

func (s *Server) patchExtensions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	f := toggles.Load(s.cfg.Home)
	f.Extensions = session.Toggle{Disabled: body.Disabled}
	if err := toggles.Save(s.cfg.Home, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Reload()
	s.getExtensions(w, r)
}

func (s *Server) extensionCatalog(snapshot resources.Snapshot) []map[string]any {
	tg := toggles.Load(s.cfg.Home)
	items := []map[string]any{}
	for _, d := range snapshot.Extensions {
		items = append(items, map[string]any{
			"name":         d.Name,
			"version":      d.Version,
			"description":  d.Description,
			"path":         d.Path,
			"source":       d.Scope,
			"enabled":      tg.Extensions.Allowed(d.Name),
			"capabilities": d.Capabilities,
			"error":        d.Error,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i]["name"].(string)
		b, _ := items[j]["name"].(string)
		return a < b
	})
	return items
}

func (s *Server) onExtensionError(sessionID, name, capability, code, message string) {
	ev := loop.Event{
		Type:        loop.ExtensionError,
		Server:      name,
		Reason:      code,
		MessageText: message,
	}
	if dir, ok := s.sidx.Lookup(sessionID); ok {
		entry, err := session.AppendSidebandEvent(dir, string(loop.ExtensionError), map[string]any{
			"extension": name, "capability": capability, "code": code, "message": message,
		})
		if err != nil {
			slog.Warn("persist extension error", "session_id", sessionID, "extension", name, "err", err)
		} else {
			ev.EntryID = entry.ID
		}
	}
	s.mu.Lock()
	if st := s.runs[sessionID]; st != nil {
		st.mu.Lock()
		st.evs = append(st.evs, ev)
		st.wait.Broadcast()
		st.mu.Unlock()
	}
	for subscriber := range s.mcpSubscribers[sessionID] {
		select {
		case subscriber <- ev:
		default:
		}
	}
	s.mu.Unlock()
}

// occupy claims exclusive run ownership for id. The caller must pair it with
// release: runPrompt defers that; doCompact calls it after compact.Run.
// A second occupy while done is still open returns 409. A finished run stays
// in s.runs until the next occupy overwrites it (SSE replay after done).
func (s *Server) occupy(parent context.Context, id string) (*runState, context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.runs[id]; ok {
		select {
		case <-st.done:
		default:
			return nil, nil, errSessionBusy
		}
	}
	ctx, cancel := context.WithCancel(parent)
	st := &runState{cancel: cancel, done: make(chan struct{})}
	st.wait = sync.NewCond(&st.mu)
	s.runs[id] = st
	return st, ctx, nil
}

// release ends occupy: cancel the run, close done so SSE drains and
// running() is false, then apply a reload queued while this run held the
// fixed request header. The finished runState stays in s.runs so late SSE
// subscribers can replay until the next occupy overwrites it. close(done)
// before Broadcast is the events-wait protocol.
func (s *Server) release(id string, st *runState) {
	if st == nil {
		return
	}
	st.cancel()
	s.mu.Lock()
	pending := s.pendingReload[id]
	delete(s.pendingReload, id)
	s.mu.Unlock()
	st.mu.Lock()
	st.steerClosed = true
	close(st.done)
	st.wait.Broadcast()
	st.mu.Unlock()
	if pending {
		s.reloadSession(id)
	}
	if s.ext != nil {
		s.ext.OnEvent(id, extension.RedactEvent(loop.Event{Type: loop.AgentSettled}, id))
	}
	s.flushSettled(id)
	s.dispatchQueue(id)
}

func (s *Server) getMessage(w http.ResponseWriter, _ *http.Request) {
	tg := toggles.Load(s.cfg.Home)
	writeJSON(w, 200, map[string]any{"busy": tg.Message.BusyDelivery()})
}

func (s *Server) patchMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Busy string `json:"busy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Busy != toggles.BusySteer && body.Busy != toggles.BusyQueue {
		http.Error(w, "busy must be steer or queue", http.StatusBadRequest)
		return
	}
	f := toggles.Load(s.cfg.Home)
	f.Message.Busy = body.Busy
	if err := toggles.Save(s.cfg.Home, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.getMessage(w, r)
}

// pushSteerRun writes Inbox on this occupy only. Using the captured runState
// avoids steering a later occupy that replaced s.runs[id] after TakeQueueID.
func (s *Server) pushSteerRun(st *runState, content []types.Content) bool {
	if st == nil {
		return false
	}
	msg := types.Message{Role: "user", Content: content}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.steerClosed || st.inbox == nil {
		return false
	}
	st.inbox.Push(msg)
	st.evs = append(st.evs, loop.Event{Type: loop.SteerAccepted, Message: &msg})
	st.wait.Broadcast()
	return true
}

func enableRunInbox(st *runState) {
	st.mu.Lock()
	st.inbox = &loop.Inbox{}
	st.steerClosed = false
	st.mu.Unlock()
}

func (s *Server) runAt(id string) *runState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func (s *Server) publishRunAborted(sessionID string) {
	s.publishSideband(sessionID, loop.Event{Type: loop.RunAborted}, true)
}

func (s *Server) publishQueueChanged(sessionID string) {
	s.publishSideband(sessionID, loop.Event{Type: loop.QueueChanged}, true)
}

func (s *Server) publishSideband(sessionID string, ev loop.Event, persist bool) {
	if persist {
		if dir, ok := s.sidx.Lookup(sessionID); ok {
			entry, err := session.AppendSidebandEvent(dir, string(ev.Type), map[string]any{})
			if err != nil {
				slog.Warn("persist sideband event", "session_id", sessionID, "type", ev.Type, "err", err)
			} else {
				ev.EntryID = entry.ID
			}
		}
	}
	s.mu.Lock()
	if st := s.runs[sessionID]; st != nil {
		st.mu.Lock()
		st.evs = append(st.evs, ev)
		st.wait.Broadcast()
		st.mu.Unlock()
	}
	for subscriber := range s.mcpSubscribers[sessionID] {
		select {
		case subscriber <- ev:
		default:
		}
	}
	s.mu.Unlock()
}

func (s *Server) dispatchQueue(id string) {
	if s.running(id) {
		return
	}
	dir, ok := s.sidx.Lookup(id)
	if !ok {
		return
	}
	item, ok, err := session.Dequeue(dir)
	if err != nil {
		slog.Warn("dequeue", "session_id", id, "err", err)
		return
	}
	if !ok {
		ext, eok, eerr := session.DequeueExtOccupy(dir)
		if eerr != nil {
			slog.Warn("dequeue ext", "session_id", id, "err", eerr)
			return
		}
		if !eok {
			return
		}
		s.publishQueueChanged(id)
		st, ctx, oerr := s.occupy(context.Background(), id)
		if oerr != nil {
			_, _ = session.EnqueueExt(dir, ext)
			s.publishQueueChanged(id)
			return
		}
		enableRunInbox(st)
		origin := ""
		if ext.Extension != "" {
			origin = "extension:" + ext.Extension
		}
		go s.runPrompt(ctx, st, id, ext.Content, nil, "", origin, nil)
		return
	}
	s.publishQueueChanged(id)
	st, ctx, err := s.occupy(context.Background(), id)
	if err != nil {
		if err := session.EnqueueFront(dir, item); err != nil {
			slog.Warn("requeue", "session_id", id, "err", err)
		} else {
			s.publishQueueChanged(id)
		}
		return
	}
	enableRunInbox(st)
	go s.runPrompt(ctx, st, id, item.Content, nil, "", "", s.takeNextTurn(id))
}

func writeHandled(w http.ResponseWriter, notice string, isErr bool) {
	writeJSON(w, 200, map[string]any{"handled": true, "notice": notice, "error": isErr})
}

func contentText(content []types.Content) string {
	var b strings.Builder
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func hasNonText(content []types.Content) bool {
	for _, c := range content {
		if c.Type != "text" {
			return true
		}
	}
	return false
}

func (s *Server) startRun(parent context.Context, w http.ResponseWriter, id string, content []types.Content, parentID *string, model string) {
	st, ctx, err := s.occupy(parent, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	enableRunInbox(st)
	// runPrompt's defer release returns this occupy slot.
	go s.runPrompt(ctx, st, id, content, parentID, model, "", s.takeNextTurn(id))
	writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "started"})
}
