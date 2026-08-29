package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"ki/internal/extension"
	"ki/internal/idgen"
	"ki/internal/loop"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/toggles"
	"ki/internal/types"
)

var (
	errSessionBusy   = errors.New("session busy")
	errRuntimeClosed = errors.New("server shutting down")
)

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
		//nolint:contextcheck // reload rewarm is process-owned via runtimeCtx
		queued = s.Reload()
	} else {
		//nolint:contextcheck // reload rewarm is process-owned via runtimeCtx
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
	//nolint:contextcheck // skill toggle reload rewarm is process-owned via runtimeCtx
	s.Reload()
	s.getSkills(w, r)
}

func (s *Server) getExtensions(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.resources.Scan("")
	_ = s.disableManifestExtensions(snapshot.Extensions)
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
	//nolint:contextcheck // extension toggle reload rewarm is process-owned via runtimeCtx
	s.Reload()
	if err := s.disableManifestExtensions(s.resources.Scan("").Extensions); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.getExtensions(w, r)
}

func (s *Server) extensionDescriptor(name string) (extension.Descriptor, bool) {
	discovery := extension.Discover(s.cfg.Home, toggles.Load(s.cfg.Home).Extensions)
	for _, d := range discovery.All {
		if d.Name == name {
			return d, true
		}
	}
	return extension.Descriptor{}, false
}

func (s *Server) getExtensionConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := s.extensionDescriptor(r.PathValue("name"))
	if !ok {
		http.Error(w, "extension not found", http.StatusNotFound)
		return
	}
	values, err := extension.SanitizedConfig(d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": d.Name, "schema": d.Config.Schema, "config": values, "i18n": d.I18n})
}

func (s *Server) patchExtensionConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := s.extensionDescriptor(r.PathValue("name"))
	if !ok {
		http.Error(w, "extension not found", http.StatusNotFound)
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	configRaw, ok := body["config"]
	if !ok {
		var err error
		configRaw, err = json.Marshal(body)
		if err != nil {
			http.Error(w, "invalid config", http.StatusBadRequest)
			return
		}
	}
	var patch map[string]any
	if err := json.Unmarshal(configRaw, &patch); err != nil || patch == nil {
		http.Error(w, "config must be a JSON object", http.StatusBadRequest)
		return
	}
	values, err := extension.UpdateConfig(d, patch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if s.ext != nil {
		s.ext.NotifyGlobal(d.Name, "config.updated", map[string]any{"config": values})
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": d.Name, "schema": d.Config.Schema, "config": values, "i18n": d.I18n})
}

func (s *Server) extensionCatalog(snapshot resources.Snapshot) []map[string]any {
	tg := toggles.Load(s.cfg.Home)
	statuses := map[string]extension.RuntimeStatus{}
	if s.ext != nil {
		for _, state := range s.ext.RuntimeStatuses() {
			statuses[state.Name] = state
		}
	}
	items := []map[string]any{}
	for _, d := range snapshot.Extensions {
		errorText := d.Error
		items = append(items, map[string]any{
			"name":         d.Name,
			"version":      d.Version,
			"description":  d.Description,
			"path":         d.Path,
			"enabled":      tg.Extensions.Allowed(d.Name),
			"capabilities": d.Capabilities,
			"configurable": len(d.Config.Schema) > 0,
		})
		if d.I18n != nil {
			items[len(items)-1]["i18n"] = d.I18n
		}
		if ui := s.globalExtensionUI(d.Name); ui != nil {
			items[len(items)-1]["ui"] = ui
		}
		if state, ok := statuses[d.Name]; ok {
			items[len(items)-1]["runtime"] = state
		}
		if errorText != "" {
			items[len(items)-1]["error"] = errorText
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i]["name"].(string)
		b, _ := items[j]["name"].(string)
		return a < b
	})
	return items
}

func (s *Server) onExtensionError(sessionID, name, capability, code, message string) {
	if sessionID != "" && (code == "manifest" || code == "sidecar_start" || code == "undeclared") {
		s.disableExtensions(name)
	}
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
	for subscriber := range s.eventSubscribers[sessionID] {
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
	s.runtimeMu.Lock()
	closed := s.runtimeClosed
	s.runtimeMu.Unlock()
	if closed {
		return nil, nil, errRuntimeClosed
	}
	if st, ok := s.runs[id]; ok {
		select {
		case <-st.done:
		default:
			return nil, nil, errSessionBusy
		}
	}
	ctx, cancel := context.WithCancel(parent)
	runID, err := idgen.NewV7()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("new run id: %w", err)
	}
	st := &runState{cancel: cancel, runID: runID, done: make(chan struct{})}
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
	st.mu.Lock()
	runID := st.runID
	external := cloneExternal(st.external)
	st.mu.Unlock()
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
		s.ext.OnEvent(context.Background(), id, extension.RedactEvent(loop.Event{Type: loop.AgentSettled, RunID: runID, External: external}, id))
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
func (s *Server) pushSteerRun(st *runState, content []types.Content, external ...map[string]string) bool {
	if st == nil {
		return false
	}
	var externalMeta map[string]string
	if len(external) > 0 {
		externalMeta = cloneExternal(external[0])
	}
	msg := types.Message{Role: "user", Content: content, External: externalMeta}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.steerClosed || st.inbox == nil {
		return false
	}
	st.inbox.Push(msg)
	st.evs = append(st.evs, loop.Event{Type: loop.SteerAccepted, Message: &msg, RunID: st.runID, External: cloneExternal(st.external)})
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
	ev := loop.Event{Type: loop.RunAborted}
	if st := s.runAt(sessionID); st != nil {
		st.mu.Lock()
		ev.RunID = st.runID
		ev.External = cloneExternal(st.external)
		st.mu.Unlock()
	}
	s.publishSideband(sessionID, ev, true)
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
	for subscriber := range s.eventSubscribers[sessionID] {
		select {
		case subscriber <- ev:
		default:
		}
	}
	s.mu.Unlock()
}

func (s *Server) dispatchQueue(id string) {
	gate := s.inputGate(id)
	gate.Lock()
	defer gate.Unlock()
	// Pin this dispatch on runtimeWG under the same lock as runtimeClosed so
	// Shutdown's Wait cannot return while dequeue→occupy is still in flight.
	s.runtimeMu.Lock()
	if s.runtimeClosed {
		s.runtimeMu.Unlock()
		return
	}
	s.runtimeWG.Add(1)
	s.runtimeMu.Unlock()
	defer s.runtimeWG.Done()
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
			if err := s.flushContextMessages(id, 0); err != nil {
				slog.Warn("flush context queue", "session_id", id, "err", err)
			}
			return
		}
		if ext.ContextBoundary {
			if ext.ContextSequence != 0 {
				if err := s.flushContextMessages(id, ext.ContextSequence); err != nil {
					if restoreErr := session.EnqueueExtFront(dir, ext); restoreErr != nil {
						slog.Warn("restore extension queue", "session_id", id, "err", restoreErr)
					}
					slog.Warn("flush context queue", "session_id", id, "err", err)
					return
				}
			}
		} else if err := s.flushContextMessages(id, 0); err != nil {
			if restoreErr := session.EnqueueExtFront(dir, ext); restoreErr != nil {
				slog.Warn("restore extension queue", "session_id", id, "err", restoreErr)
			}
			slog.Warn("flush context queue", "session_id", id, "err", err)
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
		go s.runPrompt(ctx, st, id, ext.Content, nil, "", origin, ext.IdempotencyKey, nil, ext.External)
		return
	}
	s.publishQueueChanged(id)
	if err := s.flushContextMessages(id, 0); err != nil {
		if restoreErr := session.EnqueueFront(dir, item); restoreErr != nil {
			slog.Warn("requeue", "session_id", id, "err", restoreErr)
		}
		slog.Warn("flush context queue", "session_id", id, "err", err)
		return
	}
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
	go s.runPrompt(ctx, st, id, item.Content, nil, "", "", "", s.takeNextTurn(id))
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
	gate := s.inputGate(id)
	gate.Lock()
	defer gate.Unlock()
	if err := s.flushContextMessages(id, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	st, ctx, err := s.occupy(parent, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	enableRunInbox(st)
	// runPrompt's defer release returns this occupy slot.
	go s.runPrompt(ctx, st, id, content, parentID, model, "", "", s.takeNextTurn(id))
	writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "started"})
}
