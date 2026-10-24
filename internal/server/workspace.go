package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ki/internal/session"
	"ki/internal/telemetry"
	"ki/internal/workspace"
)

func (s *Server) resolveWorkspace(id, cwd, title string) (workspace.Record, error) {
	if id != "" {
		rec, ok := s.ws.Get(id)
		if !ok {
			return workspace.Record{}, workspace.ErrNotFound
		}
		return rec, nil
	}
	if strings.TrimSpace(cwd) != "" {
		rec, _, err := s.ws.Create(cwd, title)
		if err != nil {
			return workspace.Record{}, fmt.Errorf("create workspace: %w", err)
		}
		return rec, nil
	}
	rec, err := s.ws.EnsureTemp()
	if err != nil {
		return workspace.Record{}, fmt.Errorf("create temporary workspace: %w", err)
	}
	return rec, nil
}

func (s *Server) sessionMap(sess *session.Session, extra map[string]any) map[string]any {
	entries := sess.Entries()
	return s.sessionMapData(sess.ID(), sess.Dir, sess.Header, sess.Config, session.TitleFrom(sess.Config, entries), extra)
}

func (s *Server) sessionMapSnap(snap *sessionSnap, extra map[string]any) map[string]any {
	return s.sessionMapData(snap.id, snap.dir, snap.header, snap.configV, snap.title, extra)
}

func (s *Server) sessionMapData(id, dir string, header session.Header, cfg session.Config, title string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id":                    id,
		"cwd":                   header.CWD,
		"provider":              cfg.Provider,
		"model":                 cfg.Model,
		"thinkingEffort":        cfg.ThinkingEffort,
		"dir":                   dir,
		"parentSessionId":       header.ParentSession,
		"forkMode":              header.EffectiveForkMode(),
		"title":                 title,
		"running":               s.running(id),
		"activeDescendantCount": s.activeDescendantCount(id),
		"pinned":                cfg.Pinned,
		"pinnedAt":              cfg.PinnedAt,
		"timestamp":             header.Timestamp,
		"metadata":              publicQuestionSideMetadata(cfg.Metadata),
	}
	addQuestionSideMetadata(m, cfg.Metadata)
	if s.agentTasks != nil {
		_, m["isAgent"] = s.agentTasks.TaskForSession(id)
	}
	if rec, ok := s.ws.Match(header.CWD); ok {
		m["workspaceId"] = rec.ID
	}
	maps.Copy(m, extra)
	return m
}

func (s *Server) sessionWorkspaceIDs(infos []session.Info) map[string]string {
	ids := make(map[string]string)
	for _, info := range infos {
		if _, seen := ids[info.CWD]; seen {
			continue
		}
		ids[info.CWD] = ""
		if rec, ok := s.ws.Match(info.CWD); ok {
			ids[info.CWD] = rec.ID
		}
	}
	return ids
}

func (s *Server) infoMap(info session.Info, activity sessionActivity, workspaceID string) map[string]any {
	m := map[string]any{
		"id":                    info.ID,
		"cwd":                   info.CWD,
		"dir":                   info.Dir,
		"provider":              info.Provider,
		"model":                 info.Model,
		"timestamp":             info.Timestamp,
		"updatedAt":             info.UpdatedAt,
		"parentSessionId":       info.ParentSessionID,
		"forkMode":              info.ForkMode,
		"title":                 info.Title,
		"running":               activity.running[info.ID],
		"activeDescendantCount": activity.descendants[info.ID],
		"pinned":                info.Pinned,
		"pinnedAt":              info.PinnedAt,
		"metadata":              publicQuestionSideMetadata(info.Metadata),
	}
	addQuestionSideMetadata(m, info.Metadata)
	if metadataString(info.Metadata, "purpose") != questionSidePurpose {
		links := info.QuestionLinks
		if links == nil {
			links = []session.QuestionLink{}
		}
		m["questionLinks"] = links
		m["questionLinksLoading"] = info.QuestionLinksLoading
	}
	if s.agentTasks != nil {
		_, m["isAgent"] = s.agentTasks.TaskForSession(info.ID)
	}
	if workspaceID != "" {
		m["workspaceId"] = workspaceID
	}
	return m
}

func (s *Server) workspaceJSON(rec workspace.Record) map[string]any {
	status := "ok"
	if st, err := os.Stat(rec.Path); err != nil || !st.IsDir() {
		status = "missing-dir"
	}
	return map[string]any{
		"id":         rec.ID,
		"path":       rec.Path,
		"title":      rec.Title,
		"createdAt":  rec.CreatedAt,
		"updatedAt":  rec.UpdatedAt,
		"status":     status,
		"temp":       s.ws.IsTemp(rec),
		"sessionIds": rec.SessionIDs,
	}
}

func (s *Server) listWorkspaces(w http.ResponseWriter, _ *http.Request) {
	recs := s.ws.List()
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		out = append(out, s.workspaceJSON(rec))
	}
	writeJSON(w, 200, out)
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	rec, created, err := s.ws.Create(body.Path, body.Title)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := 200
	if created {
		code = 201
	}
	s.publishInvalidation(scopeWorkspaces)
	s.publishInvalidation(scopeSessions)
	writeJSON(w, code, s.workspaceJSON(rec))
}

func (s *Server) patchWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := s.ws.SetTitle(r.PathValue("id"), body.Title); err != nil {
		code := 400
		if workspace.NotFound(err) {
			code = 404
		}
		http.Error(w, err.Error(), code)
		return
	}
	rec, _ := s.ws.Get(r.PathValue("id"))
	s.publishInvalidation(scopeWorkspaces)
	writeJSON(w, 200, s.workspaceJSON(rec))
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	finish, err := s.beginDeletion()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer finish()
	id := r.PathValue("id")
	s.creationMu.Lock()
	rec, ok := s.ws.Get(id)
	if !ok {
		s.creationMu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.deletingWorkspaces[id] = true
	s.creationMu.Unlock()
	infos, err := session.List(s.cfg.Sessions.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var targets []session.Info
	var reservations []<-chan struct{}
	for _, info := range infos {
		if m, ok := s.ws.Match(info.CWD); ok && m.ID == rec.ID {
			release, drained, err := s.fenceAgentDeletion(info.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer release()
			reservations = append(reservations, drained)
		}
	}
	seen := map[string]bool{}
	for {
		infos, err = session.List(s.cfg.Sessions.Root)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var added []session.Info
		for _, info := range infos {
			if m, ok := s.ws.Match(info.CWD); ok && m.ID == rec.ID && !seen[info.ID] {
				seen[info.ID] = true
				added = append(added, info)
				targets = append(targets, info)
				release, drained, err := s.fenceAgentDeletion(info.ID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				defer release()
				reservations = append(reservations, drained)
			}
		}
		if len(added) == 0 {
			break
		}
		s.fenceDeletionInputs(added)
	}
	if err := s.deleteSessionInfos(targets, reservations); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := s.ws.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) moveWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BeforeID *string `json:"beforeId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	before := ""
	if body.BeforeID != nil {
		before = *body.BeforeID
	}
	if err := s.ws.InsertBefore(r.PathValue("id"), before); err != nil {
		code := 400
		if workspace.NotFound(err) {
			code = 404
		}
		http.Error(w, err.Error(), code)
		return
	}
	s.publishInvalidation(scopeWorkspaces)
	s.listWorkspaces(w, r)
}

func (s *Server) moveWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string  `json:"sessionId"`
		BeforeID  *string `json:"beforeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SessionID == "" {
		http.Error(w, "sessionId required", http.StatusBadRequest)
		return
	}
	before := ""
	if body.BeforeID != nil {
		before = *body.BeforeID
	}
	id := r.PathValue("id")
	if rec, ok := s.ws.Get(id); ok {
		found := slices.Contains(rec.SessionIDs, body.SessionID)
		if !found {
			http.Error(w, "session not in workspace", http.StatusBadRequest)
			return
		}
	}
	if err := s.ws.InsertSessionBefore(id, body.SessionID, before); err != nil {
		code := 400
		if workspace.NotFound(err) {
			code = 404
		}
		http.Error(w, err.Error(), code)
		return
	}
	rec, _ := s.ws.Get(id)
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
	writeJSON(w, 200, s.workspaceJSON(rec))
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	finish, err := s.beginDeletion()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer finish()
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	targetID := sess.ID()
	target := session.Info{
		ID:              sess.ID(),
		CWD:             sess.Header.CWD,
		Dir:             sess.Dir,
		ParentSessionID: sess.Header.ParentSession,
		ForkMode:        sess.Header.EffectiveForkMode(),
	}
	_ = sess.Close()
	release, drained, err := s.fenceAgentDeletion(targetID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer release()
	s.markDeleting([]string{targetID})

	var targets []session.Info
	fenced := map[string]bool{}
	for {
		infos, err := session.List(s.cfg.Sessions.Root)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		targets = s.collectSessionTree(infos, target)
		var added []session.Info
		for _, info := range targets {
			if !fenced[info.ID] {
				fenced[info.ID] = true
				added = append(added, info)
			}
		}
		if len(added) == 0 {
			break
		}
		s.fenceDeletionInputs(added)
	}
	slices.Reverse(targets)
	if err := s.deleteSessionInfos(targets, []<-chan struct{}{drained}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) collectSessionTree(infos []session.Info, target session.Info) []session.Info {
	byID := make(map[string]session.Info, len(infos)+1)
	for _, info := range infos {
		byID[info.ID] = info
	}
	byID[target.ID] = target
	children := make(map[string][]session.Info)
	for _, info := range infos {
		if metadataString(info.Metadata, "purpose") == questionSidePurpose {
			sourceID := metadataString(info.Metadata, "sourceSessionId")
			if _, ok := byID[sourceID]; ok {
				children[sourceID] = append(children[sourceID], info)
			}
			continue
		}
		if info.ParentSessionID == "" || info.ForkMode != session.ForkModeTree {
			continue
		}
		parent, ok := byID[info.ParentSessionID]
		if !ok || !sameSessionWorkspace(s, parent, info) {
			continue
		}
		children[info.ParentSessionID] = append(children[info.ParentSessionID], info)
	}
	var targets []session.Info
	seen := make(map[string]bool)
	var collect func(string)
	collect = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		info, ok := byID[id]
		if !ok {
			return
		}
		targets = append(targets, info)
		for _, child := range children[id] {
			collect(child.ID)
		}
	}
	collect(target.ID)
	return targets
}

func sameSessionWorkspace(s *Server, a, b session.Info) bool {
	ar, aok := s.ws.Match(a.CWD)
	br, bok := s.ws.Match(b.CWD)
	if aok && bok {
		return ar.ID == br.ID
	}
	return filepath.Clean(a.CWD) == filepath.Clean(b.CWD)
}

func (s *Server) removeSessionInfo(info session.Info) error {
	s.abortRun(info.ID)
	s.closeProcesses(info.ID)
	if s.agentTasks != nil {
		s.agentTasks.RemoveSession(info.ID)
	}
	if rec, ok := s.ws.Match(info.CWD); ok {
		_ = s.ws.DetachSession(rec.ID, info.ID)
	}
	s.dropSessionSnap(info.ID)
	telemetry.Forget(info.Dir)
	if err := session.Remove(info.Dir); err != nil {
		return fmt.Errorf("remove session: %w", err)
	}
	s.sidx.Remove(info.ID)
	s.mu.Lock()
	s.forgetReplayLocked(info.ID)
	if st := s.runs[info.ID]; st != nil {
		select {
		case <-st.done:
			delete(s.runs, info.ID)
		default:
		}
	}
	s.mu.Unlock()
	s.resources.Invalidate(info.ID)
	if s.ext != nil {
		s.ext.CloseSession(info.ID)
	}
	s.resetRuntime(info.ID)
	return nil
}

func (s *Server) abortRun(id string) {
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st == nil {
		return
	}
	s.cancelRun(id, st, cancelReasonSessionDelete, "server", false)
	if st.released != nil {
		<-st.released
	} else {
		<-st.done
	}
}

func (s *Server) searchSessions(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "q required", http.StatusBadRequest)
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	hits, hasMore, err := session.Search(s.cfg.Sessions.Root, session.SearchOptions{
		Context: r.Context(), Query: q, Limit: limit, IncludeAgents: queryBool(r.URL.Query().Get("includeAgents")),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type hit struct {
		ID             string `json:"id"`
		Title          string `json:"title"`
		CWD            string `json:"cwd,omitempty"`
		Model          string `json:"model,omitempty"`
		UpdatedAt      string `json:"updatedAt,omitempty"`
		WorkspaceID    string `json:"workspaceId,omitempty"`
		WorkspaceTitle string `json:"workspaceTitle,omitempty"`
		Snippet        string `json:"snippet,omitempty"`
	}
	items := make([]hit, 0, len(hits))
	for _, found := range hits {
		if r.Context().Err() != nil {
			return
		}
		h := hit{
			ID: found.ID, Title: found.Title, CWD: found.CWD, Model: found.Model,
			UpdatedAt: found.UpdatedAt, Snippet: found.Snippet,
		}
		if rec, ok := s.ws.Match(found.CWD); ok {
			h.WorkspaceID = rec.ID
			h.WorkspaceTitle = rec.Title
		}
		items = append(items, h)
	}
	writeJSON(w, 200, map[string]any{"items": items, "hasMore": hasMore})
}
