package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"ki/internal/command"
	"ki/internal/extension"
	"ki/internal/idgen"
	"ki/internal/loop"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/toggles"
	"ki/internal/tools"
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
	// Both branches change what /v1/extensions and the open session's runtime
	// catalog report; a per-session reload has no other refetch signal.
	s.publishInvalidation(scopeExtensions)
	s.publishInvalidation(scopeSessions)
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

// getTools returns the built-in tool catalog for the selected session/model.
// The global toggle is still shared across sessions; the model only controls
// the rich/text Read mode, since every model edits with Write/Edit.
func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	var profile tools.Profile
	if sessionID != "" {
		sess, err := s.open(sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer func() { _ = sess.Close() }()
		cwd = sess.Header.CWD
		_, info, ok := s.registry.FindModel(sess.Config.Provider, sess.Config.Model)
		if !ok {
			http.Error(w, "session model unavailable", http.StatusUnprocessableEntity)
			return
		}
		profile.RichRead = slices.Contains(info.Input, "image")
		// Why this catalog does not resolve Agent depth: the tool list shown to
		// clients must match the tool list sent to the provider, which is
		// deliberately independent of the session's Agent depth. A deep session
		// still reports Agent; the spawn call itself refuses past the limit.
	} else if ref := s.registry.Default(); ref.Provider != "" && ref.Model != "" {
		// Settings can be opened before a session exists. Use the last selected
		// model when it is available, while retaining a useful fallback catalog
		// if the provider catalog is not configured yet.
		if _, info, ok := s.registry.FindModel(ref.Provider, ref.Model); ok {
			profile.RichRead = slices.Contains(info.Input, "image")
		}
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd == "" {
		cwd = "."
	}
	builtins := (tools.Set{
		CWD: cwd, Jobs: s.jobsFor(sessionID), Agent: s,
		AgentParentSessionID: sessionID, Shells: s.shells, Mutations: s.mutations,
	}).Build(profile)
	tg := toggles.Load(s.cfg.Home)
	items := make([]map[string]any, 0, len(builtins))
	for _, tool := range builtins {
		items = append(items, map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"source":      "builtin",
			"enabled":     tg.Tools.Allowed(tool.Name()),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) patchTools(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	f := toggles.Load(s.cfg.Home)
	f.Tools = session.Toggle{Disabled: body.Disabled}
	if err := toggles.Save(s.cfg.Home, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The current run keeps its request header; Reload queues the new tool
	// set for an occupied session and applies it at the next occupy boundary.
	s.Reload()
	s.getTools(w, r)
}

func (s *Server) getCommands(w http.ResponseWriter, r *http.Request) {
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	snapshot := s.resources.Scan(cwd)
	tg := toggles.Load(s.cfg.Home)
	items := command.Catalog(snapshot, tg.Skills)
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
	writeJSON(w, 200, map[string]any{"items": s.extensionCatalog(snapshot, "")})
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
	s.publishInvalidation(scopeExtensions)
	s.publishInvalidation(scopeSessions)
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

func (s *Server) extensionCatalog(snapshot resources.Snapshot, sessionID string) []map[string]any {
	tg := toggles.Load(s.cfg.Home)
	statuses := map[string]extension.RuntimeStatus{}
	contribs := map[string]extension.SessionContribution{}
	if s.ext != nil {
		for _, state := range s.ext.RuntimeStatuses() {
			statuses[state.Name] = state
		}
		contribs = s.ext.SessionContributions(sessionID)
	}
	skillsByExt := map[string][]map[string]any{}
	for _, item := range snapshot.Skills {
		name, ok := strings.CutPrefix(item.Source, "extension:")
		if !ok {
			continue
		}
		skillsByExt[name] = append(skillsByExt[name], map[string]any{
			"name":        item.Name,
			"description": item.Description,
		})
	}
	cmdsByExt := map[string][]map[string]any{}
	seenCmd := map[string]map[string]bool{}
	for _, tmpl := range snapshot.Prompts {
		if tmpl.Extension == "" {
			continue
		}
		cmdsByExt[tmpl.Extension] = append(cmdsByExt[tmpl.Extension], map[string]any{
			"name":        tmpl.Name,
			"description": tmpl.Description,
			"source":      "prompt",
		})
		if seenCmd[tmpl.Extension] == nil {
			seenCmd[tmpl.Extension] = map[string]bool{}
		}
		seenCmd[tmpl.Extension][tmpl.Name] = true
	}
	for name, contrib := range contribs {
		for _, cmd := range contrib.Commands {
			if seenCmd[name][cmd.Name] {
				continue
			}
			cmdsByExt[name] = append(cmdsByExt[name], map[string]any{
				"name":        cmd.Name,
				"description": cmd.Description,
				"source":      "runtime",
			})
			if seenCmd[name] == nil {
				seenCmd[name] = map[string]bool{}
			}
			seenCmd[name][cmd.Name] = true
		}
	}
	for name := range skillsByExt {
		slices.SortFunc(skillsByExt[name], func(a, b map[string]any) int { return cmp.Compare(mapName(a), mapName(b)) })
	}
	for name := range cmdsByExt {
		slices.SortFunc(cmdsByExt[name], func(a, b map[string]any) int { return cmp.Compare(mapName(a), mapName(b)) })
	}
	items := []map[string]any{}
	for _, d := range snapshot.Extensions {
		errorText := d.Error
		item := map[string]any{
			"name":         d.Name,
			"version":      d.Version,
			"description":  d.Description,
			"path":         d.Path,
			"enabled":      tg.Extensions.Allowed(d.Name),
			"capabilities": d.Capabilities,
			"configurable": len(d.Config.Schema) > 0,
		}
		if d.I18n != nil {
			item["i18n"] = d.I18n
		}
		if ui := s.globalExtensionUI(d.Name); ui != nil {
			item["ui"] = ui
		}
		if state, ok := statuses[d.Name]; ok {
			item["runtime"] = state
		}
		if errorText != "" {
			item["error"] = errorText
		}
		if sk := skillsByExt[d.Name]; len(sk) > 0 {
			item["skills"] = sk
		}
		if contrib := contribs[d.Name]; len(contrib.Tools) > 0 {
			item["tools"] = contrib.Tools
		}
		if cmds := cmdsByExt[d.Name]; len(cmds) > 0 {
			item["commands"] = cmds
		}
		if files := d.PromptAppendFiles(); len(files) > 0 {
			item["promptAppend"] = files
		}
		// PATH contributions are shown with their current existence: an enabled
		// package whose directory never appears is invisible otherwise, and PATH
		// shadowing has to stay observable.
		if dirs := d.PathDirStatuses(); len(dirs) > 0 {
			item["pathDirs"] = dirs
		}
		if len(d.Providers) > 0 {
			providers := make([]map[string]any, 0, len(d.Providers))
			for _, p := range d.Providers {
				providers = append(providers, map[string]any{"id": p.ID, "name": p.Name, "api": p.API})
			}
			item["providers"] = providers
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b map[string]any) int { return cmp.Compare(mapName(a), mapName(b)) })
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
	s.mu.Unlock()
	s.publishPush(sessionID, ev)
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
	// Green dot for every other client, not just the one that prompted.
	s.publishInvalidation(scopeSessions)
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
	// Publish after dispatchQueue: it may occupy again for a queued message, and
	// the last frame then already reflects the next run starting.
	s.dispatchQueue(id)
	s.publishInvalidation(scopeSessions)
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

// steerRequest is one user-role message injected into a live run. Origin marks
// who produced it (empty is the human, agent:<task-id> a subagent or its
// completion notification, extension:<name> a relayed message); External
// carries the metadata a relaying extension needs to correlate the turn.
type steerRequest struct {
	Content  []types.Content
	Origin   string
	External map[string]string
}

// pushSteerRun writes Inbox on this occupy only. Using the captured runState
// avoids steering a later occupy that replaced s.runs[id] after TakeQueueID.
//
// A false return means the message did not enter this run: the caller must
// deliver it another way (durable queue) rather than drop it.
func (s *Server) pushSteerRun(st *runState, req steerRequest) bool {
	if st == nil {
		return false
	}
	msg := types.Message{Role: "user", Content: req.Content, Origin: req.Origin, External: cloneExternal(req.External)}
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
	s.mu.Unlock()
	// Fan out after releasing s.mu: push subscribers never block, but keeping
	// the two locks unnested documents the order for future publishers.
	s.publishPush(sessionID, ev)
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
	item, ok, err := s.dequeueDispatchable(id, dir)
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
	go s.runPrompt(ctx, st, id, item.Content, nil, "", item.Origin, "", s.takeNextTurn(id))
}

// dequeueDispatchable takes the next turn that should run, skipping completion
// notifications the parent already read on its own.
//
// Why here and not when the notification is enqueued: the child finishes and
// enqueues its notification while the parent is still busy, and the parent's own
// TaskOutput can read the same result before its turn ends. The queue item waits
// for that turn boundary, so this is the first moment the consumed mark exists
// and can be honored. Dropped items are not silently removed from the client's
// view: each skip republishes the queue.
func (s *Server) dequeueDispatchable(id, dir string) (session.QueuedItem, bool, error) {
	for {
		item, ok, err := session.Dequeue(dir)
		if err != nil || !ok {
			return session.QueuedItem{}, ok, err
		}
		if item.AgentTask != "" && s.agentTasks != nil && s.agentTasks.NotificationConsumed(item.AgentTask) {
			s.publishQueueChanged(id)
			continue
		}
		return item, true, nil
	}
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
	return slices.ContainsFunc(content, func(c types.Content) bool { return c.Type != "text" })
}

// mapName returns a catalog row's "name" field, treating a non-string as empty
// so sorting stays total.
func mapName(row map[string]any) string {
	name, _ := row["name"].(string)
	return name
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
