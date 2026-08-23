package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"ki/internal/mcp"
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
	queued := false
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
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	tg := toggles.Load(s.cfg.Home)
	file := s.resources.Scan(cwd).MCP
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

func (s *Server) occupy(id string, parent context.Context) (*runState, context.Context, error) {
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

func (s *Server) release(id string, st *runState) {
	if st == nil {
		return
	}
	st.cancel()
	s.mu.Lock()
	if s.runs[id] == st {
		delete(s.runs, id)
	}
	pending := s.pendingReload[id]
	delete(s.pendingReload, id)
	s.mu.Unlock()
	close(st.done)
	st.wait.L.Lock()
	st.wait.Broadcast()
	st.wait.L.Unlock()
	if pending {
		s.reloadSession(id)
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
	for _, c := range content {
		if c.Type != "text" {
			return true
		}
	}
	return false
}

func (s *Server) startRun(w http.ResponseWriter, id string, content []types.Content, parentID *string, model string) {
	st, ctx, err := s.occupy(id, context.Background())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	//nolint:contextcheck // prompt outlives the HTTP request; abort is the cancel path
	go s.runPrompt(ctx, st, id, content, parentID, model)
	writeJSON(w, 202, map[string]any{"session_id": id, "accepted": true})
}
