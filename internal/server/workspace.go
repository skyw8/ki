package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"ki/internal/session"
	"ki/internal/workspace"
)

func (s *Server) resolveWorkspace(id, cwd string) (workspace.Record, error) {
	if id != "" {
		rec, ok := s.ws.Get(id)
		if !ok {
			return workspace.Record{}, workspace.ErrNotFound
		}
		return rec, nil
	}
	if strings.TrimSpace(cwd) != "" {
		rec, _, err := s.ws.Create(cwd, "")
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
	m := map[string]any{
		"id":             sess.ID(),
		"cwd":            sess.Header.CWD,
		"provider":       sess.Config.Provider,
		"model":          sess.Config.Model,
		"thinkingEffort": sess.Config.ThinkingEffort,
		"dir":            sess.Dir,
		"parent":         sess.Header.ParentSession,
		"title":          session.TitleOf(sess),
		"running":        s.running(sess.ID()),
		"pinned":         sess.Config.Pinned,
		"pinnedAt":       sess.Config.PinnedAt,
		"timestamp":      sess.Header.Timestamp,
	}
	if rec, ok := s.ws.Match(sess.Header.CWD); ok {
		m["workspaceId"] = rec.ID
	}
	maps.Copy(m, extra)
	return m
}

func (s *Server) infoMap(info session.Info) map[string]any {
	m := map[string]any{
		"id":        info.ID,
		"cwd":       info.CWD,
		"dir":       info.Dir,
		"provider":  info.Provider,
		"model":     info.Model,
		"timestamp": info.Timestamp,
		"parent":    info.ParentSession,
		"title":     info.Title,
		"running":   s.running(info.ID),
		"pinned":    info.Pinned,
		"pinnedAt":  info.PinnedAt,
	}
	if rec, ok := s.ws.Match(info.CWD); ok {
		m["workspaceId"] = rec.ID
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
	writeJSON(w, 200, s.workspaceJSON(rec))
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, ok := s.ws.Get(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	infos, err := session.List(s.cfg.Sessions.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, info := range infos {
		if m, ok := s.ws.Match(info.CWD); ok && m.ID == rec.ID {
			s.abortRun(info.ID)
			if err := session.Remove(info.Dir); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.resources.Invalidate(info.ID)
			s.mcp.CloseSession(info.ID)
		}
	}
	if _, err := s.ws.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	writeJSON(w, 200, s.workspaceJSON(rec))
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	id := sess.ID()
	dir := sess.Dir
	cwd := sess.Header.CWD
	_ = sess.Close()
	s.abortRun(id)
	s.closeJobs(id)
	if rec, ok := s.ws.Match(cwd); ok {
		_ = s.ws.DetachSession(rec.ID, id)
	}
	if err := session.Remove(dir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.sidx.Remove(id) // only after the dir is actually gone
	s.resources.Invalidate(id)
	s.mcp.CloseSession(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) abortRun(id string) {
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st == nil {
		return
	}
	st.cancel()
	<-st.done
}

func (s *Server) searchSessions(w http.ResponseWriter, r *http.Request) {
	q := strings.ReplaceAll(r.URL.Query().Get("q"), "\x00", "")
	q = strings.TrimSpace(q)
	if q == "" {
		http.Error(w, "q required", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(q) > 500 {
		q = string([]rune(q)[:500])
	}
	ql := strings.ToLower(q)
	infos, err := session.List(s.cfg.Sessions.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	const limit = 20
	type hit struct {
		ID             string `json:"id"`
		Title          string `json:"title"`
		WorkspaceID    string `json:"workspaceId,omitempty"`
		WorkspaceTitle string `json:"workspaceTitle,omitempty"`
		Snippet        string `json:"snippet,omitempty"`
	}
	var items []hit
	hasMore := false
	for _, info := range infos {
		if r.Context().Err() != nil {
			return
		}
		sess, err := session.Open(info.Dir)
		if err != nil {
			continue
		}
		snip, ok := sessionSnippet(sess, ql)
		_ = sess.Close()
		if !ok {
			continue
		}
		if len(items) >= limit {
			hasMore = true
			break
		}
		h := hit{ID: info.ID, Title: info.Title, Snippet: snip}
		if rec, ok := s.ws.Match(info.CWD); ok {
			h.WorkspaceID = rec.ID
			h.WorkspaceTitle = rec.Title
		}
		items = append(items, h)
	}
	if items == nil {
		items = []hit{}
	}
	writeJSON(w, 200, map[string]any{"items": items, "hasMore": hasMore})
}

func sessionSnippet(sess *session.Session, ql string) (string, bool) {
	for _, e := range sess.Entries() {
		if e.Type != "message" || e.Message == nil {
			continue
		}
		if e.Message.Role != "user" && e.Message.Role != "assistant" {
			continue
		}
		var b strings.Builder
		b.WriteString(e.Message.Text())
		for _, c := range e.Message.Content {
			if c.Thinking != "" {
				b.WriteByte('\n')
				b.WriteString(c.Thinking)
			}
		}
		if snip, ok := snippetAround(b.String(), ql); ok {
			return snip, true
		}
	}
	return "", false
}

func snippetAround(text, ql string) (string, bool) {
	low := strings.ToLower(text)
	i := strings.Index(low, ql)
	if i < 0 {
		return "", false
	}
	runes := []rune(text)
	// map byte index to rune index approximately via prefix
	prefix := []rune(text[:i])
	start := max(len(prefix)-40, 0)
	end := min(len(prefix)+utf8.RuneCountInString(ql)+40, len(runes))
	s := strings.Join(strings.Fields(string(runes[start:end])), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s, true
}
