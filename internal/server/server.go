package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"ki/internal/command"
	"ki/internal/compact"
	"ki/internal/config"
	"ki/internal/logging"
	"ki/internal/loop"
	"ki/internal/mcp"
	"ki/internal/prompt"
	"ki/internal/provider"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/toggles"
	"ki/internal/tools"
	"ki/internal/types"
	"ki/internal/workspace"
)

// Options constructs a Server.
type Options struct {
	Config   config.Config
	Token    string
	Streamer loop.Streamer
	Registry *provider.Registry
}

// Server is the HTTP API.
type Server struct {
	cfg                    config.Config
	token                  string
	streamer               loop.Streamer
	registry               *provider.Registry
	requireModelCredential bool
	mcp                    *mcp.Pool
	resources              *resources.Loader
	mu                     sync.Mutex
	runs                   map[string]*runState
	jobs                   map[string]*tools.JobStore
	ws                     *workspace.Store
	sidx                   *session.Index
	ln                     net.Listener
	http                   *http.Server
	shells                 tools.ShellRuntime
	mutations              *tools.MutationQueue
}

type runState struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	evs    []loop.Event
	wait   *sync.Cond
	done   chan struct{}
	err    error
}

// File is ~/.ki/server.json
type File struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// New builds a server (does not listen).
func New(opt Options) (*Server, error) {
	shells := tools.DiscoverShellRuntime()
	tok := opt.Token
	if tok == "" {
		tok = newToken()
	}
	reg := opt.Registry
	if reg == nil {
		var err error
		reg, err = provider.NewRegistry(opt.Config.Home)
		if err != nil {
			return nil, fmt.Errorf("load provider registry: %w", err)
		}
	}
	requireCredential := opt.Streamer == nil
	st := opt.Streamer
	if st == nil {
		st = liveFromRegistry(reg)
	}
	ws := workspace.Open(opt.Config.Home, opt.Config.Sessions.Root)
	infos, _ := session.List(opt.Config.Sessions.Root)
	cwds := make([]string, 0, len(infos))
	for _, info := range infos {
		cwds = append(cwds, info.CWD)
	}
	_ = ws.Bootstrap(cwds)
	sidx := session.NewIndex(infos) // reuse the List walk: zero extra reads
	return &Server{
		cfg:                    opt.Config,
		token:                  tok,
		streamer:               st,
		registry:               reg,
		requireModelCredential: requireCredential,
		mcp:                    mcp.NewPool(opt.Config.Home), // serve-wide; not per session / per prompt
		resources:              resources.NewLoader(opt.Config.Home),
		runs:                   map[string]*runState{},
		jobs:                   map[string]*tools.JobStore{},
		ws:                     ws,
		sidx:                   sidx,
		shells:                 shells,
		mutations:              tools.NewMutationQueue(),
	}, nil
}

func liveFromRegistry(reg *provider.Registry) loop.Streamer {
	return &router{registry: reg}
}

type router struct{ registry *provider.Registry }

func (r *router) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	_, m, key, err := r.registry.Resolve(req.Provider, req.Model)
	if err != nil {
		return types.Message{}, err
	}
	msg, err := provider.NewLiveModel(m, key, nil).Stream(ctx, req, emit)
	if err != nil {
		return msg, fmt.Errorf("stream live provider: %w", err)
	}
	return msg, nil
}

func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Token is the bearer secret.
func (s *Server) Token() string { return s.token }

// Reload drops every session's resource snapshots and MCP connections so the
// next prompt/GET rebuilds resources. Process-wide: there is no per-session reload.
func (s *Server) Reload() {
	s.resources.InvalidateAll()
	if s.mcp != nil {
		s.mcp.Close()
	}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	api.HandleFunc("GET /v1/models", s.auth(s.models))
	api.HandleFunc("GET /v1/providers", s.auth(s.providers))
	api.HandleFunc("POST /v1/providers", s.auth(s.createProvider))
	api.HandleFunc("PATCH /v1/providers/{id}", s.auth(s.patchProvider))
	api.HandleFunc("DELETE /v1/providers/{id}", s.auth(s.deleteProvider))
	api.HandleFunc("PUT /v1/providers/{id}/credential", s.auth(s.putProviderCredential))
	api.HandleFunc("POST /v1/providers/{id}/models", s.auth(s.createProviderModel))
	api.HandleFunc("PATCH /v1/providers/{id}/models", s.auth(s.patchProviderModel))
	api.HandleFunc("DELETE /v1/providers/{id}/models", s.auth(s.deleteProviderModel))
	api.HandleFunc("PUT /v1/default-model", s.auth(s.putDefaultModel))
	api.HandleFunc("GET /v1/meta", s.auth(s.meta))
	api.HandleFunc("GET /v1/sessions", s.auth(s.list))
	api.HandleFunc("POST /v1/sessions", s.auth(s.create))
	api.HandleFunc("GET /v1/sessions/search", s.auth(s.searchSessions))
	api.HandleFunc("GET /v1/sessions/{id}", s.auth(s.get))
	api.HandleFunc("PATCH /v1/sessions/{id}", s.auth(s.patch))
	api.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.deleteSession))
	api.HandleFunc("POST /v1/sessions/{id}/prompt", s.auth(s.prompt))
	api.HandleFunc("GET /v1/sessions/{id}/events", s.auth(s.events))
	api.HandleFunc("POST /v1/sessions/{id}/abort", s.auth(s.abort))
	api.HandleFunc("POST /v1/sessions/{id}/compact", s.auth(s.doCompact))
	api.HandleFunc("POST /v1/sessions/{id}/fork", s.auth(s.fork))
	api.HandleFunc("POST /v1/sessions/{id}/attachments", s.auth(s.uploadAttachment))
	api.HandleFunc("POST /v1/reload", s.auth(s.doReload))
	api.HandleFunc("GET /v1/skills", s.auth(s.getSkills))
	api.HandleFunc("PATCH /v1/skills", s.auth(s.patchSkills))
	api.HandleFunc("GET /v1/mcp", s.auth(s.getMCP))
	api.HandleFunc("PATCH /v1/mcp", s.auth(s.patchMCP))
	api.HandleFunc("GET /v1/workspaces", s.auth(s.listWorkspaces))
	api.HandleFunc("POST /v1/workspaces", s.auth(s.createWorkspace))
	api.HandleFunc("PATCH /v1/workspaces/{id}", s.auth(s.patchWorkspace))
	api.HandleFunc("DELETE /v1/workspaces/{id}", s.auth(s.deleteWorkspace))
	api.HandleFunc("POST /v1/workspaces/{id}/move", s.auth(s.moveWorkspace))
	api.HandleFunc("POST /v1/workspaces/{id}/sessions/move", s.auth(s.moveWorkspaceSession))
	api.HandleFunc("GET /v1/fs", s.auth(s.listFS))
	api.HandleFunc("POST /v1/fs", s.auth(s.createFS))
	return recoverHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			api.ServeHTTP(w, r)
			return
		}
		s.serveUI(w, r)
	}))
}

func recoverHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			// Recover here so handler panics become structured JSONL records instead
			// of net/http's stderr-only unstructured output.
			if logging.Recover("http handler panic", "method", r.Method, "path", r.URL.Path) {
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got == "" || got != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// ListenAndServe binds addr (empty uses cfg).
func (s *Server) ListenAndServe(addr string) error {
	if addr == "" {
		addr = s.cfg.Server.Addr
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	httpSrv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.mu.Lock()
	s.ln = ln
	s.http = httpSrv
	s.mu.Unlock()
	if err := WriteServerFile(s.cfg.Home, File{Addr: ln.Addr().String(), Token: s.token}); err != nil {
		slog.Warn("server.json", "err", err)
	}
	slog.Info("listen", "addr", ln.Addr().String())
	return httpSrv.Serve(ln)
}

// Addr is the bound address.
func (s *Server) Addr() string {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return ""
	}
	return ln.Addr().String()
}

// Shutdown stops the HTTP server and MCP clients (pool children outlive a prompt).
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	jobs := make([]*tools.JobStore, 0, len(s.jobs))
	for id, store := range s.jobs {
		jobs = append(jobs, store)
		delete(s.jobs, id)
	}
	s.mu.Unlock()
	for _, store := range jobs {
		store.Close()
	}
	if s.mcp != nil {
		s.mcp.Close()
	}
	s.mu.Lock()
	httpSrv := s.http
	s.mu.Unlock()
	if httpSrv == nil {
		return nil
	}
	return httpSrv.Shutdown(ctx)
}

func (s *Server) jobsFor(id string) *tools.JobStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if jobs, ok := s.jobs[id]; ok {
		return jobs
	}
	jobs := tools.NewJobStore()
	s.jobs[id] = jobs
	return jobs
}

func (s *Server) closeJobs(id string) {
	s.mu.Lock()
	jobs := s.jobs[id]
	delete(s.jobs, id)
	s.mu.Unlock()
	if jobs != nil {
		jobs.Close()
	}
}

// WriteServerFile persists addr+token.
func WriteServerFile(home string, f File) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "server.json"), append(b, '\n'), 0o600)
}

// ReadServerFile loads ~/.ki/server.json.
func ReadServerFile(home string) (File, error) {
	//nolint:gosec // home is the configured private Ki directory.
	b, err := os.ReadFile(filepath.Join(home, "server.json"))
	if err != nil {
		return File{}, err
	}
	var f File
	err = json.Unmarshal(b, &f)
	return f, err
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CWD            string `json:"cwd"`
		WorkspaceID    string `json:"workspaceId"`
		Model          string `json:"model"`
		ThinkingEffort string `json:"thinkingEffort"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	rec, err := s.resolveWorkspace(body.WorkspaceID, body.CWD)
	if err != nil {
		code := 400
		if workspace.NotFound(err) {
			code = 404
		}
		http.Error(w, err.Error(), code)
		return
	}
	ref, selectedModel, err := s.registry.ResolveSpec(body.Model, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	root := s.cfg.Sessions.Root
	effort, err := resolveThinking(selectedModel, body.ThinkingEffort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	sess, err := session.Create(root, rec.Path, ref.Provider, ref.Model, effort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = sess.Close() }()
	_ = s.ws.AttachSession(rec.ID, sess.ID())
	s.sidx.Add(sess.ID(), sess.Dir)
	s.rememberModel(ref)
	writeJSON(w, 200, s.sessionMap(sess, nil))
}

func (s *Server) open(id string) (*session.Session, error) {
	root := s.cfg.Sessions.Root
	if dir, ok := s.sidx.Lookup(id); ok {
		sess, err := session.Open(dir)
		if err == nil {
			return sess, nil
		}
		// Stale entry (dir deleted or renamed outside the server): drop it so
		// the next lookup rescans instead of failing on a dead path forever.
		s.sidx.Remove(id)
		return nil, fmt.Errorf("open indexed session: %w", err)
	}
	// Index miss (session created by another process, or stale): fall back to
	// a scan, then self-heal so the next lookup is O(1).
	dir, err := session.Find(root, id)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	s.sidx.Add(id, dir)
	sess, err := session.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	return sess, nil
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	sk, mc := s.sessionCatalog(snapshot)
	tg := toggles.Load(s.cfg.Home)
	writeJSON(w, 200, s.sessionMap(sess, map[string]any{
		"leafId":          sess.LeafID(),
		"entries":         sess.Entries(),
		"messages":        sess.MessagesToLeaf(),
		"availableSkills": sk,
		"availableMcp":    mc,
		"commands":        command.Catalog(snapshot, tg.Skills),
	}))
}

func (s *Server) sessionCatalog(snapshot resources.Snapshot) ([]map[string]any, []map[string]any) {
	tg := toggles.Load(s.cfg.Home)
	sk := []map[string]any{}
	for _, item := range snapshot.Skills {
		sk = append(sk, map[string]any{
			"name":        item.Name,
			"description": item.Description,
			"path":        item.FilePath,
			"source":      item.Source,
			"enabled":     tg.Skills.Allowed(item.Name),
		})
	}
	sort.Slice(sk, func(i, j int) bool {
		a, _ := sk[i]["name"].(string)
		b, _ := sk[j]["name"].(string)
		return a < b
	})
	file := snapshot.MCP
	mc := []map[string]any{}
	for _, item := range mcp.List(file, tg.MCP) {
		row := map[string]any{
			"name":    item.Name,
			"command": item.Command,
			"source":  item.Source,
			"enabled": item.Enabled,
			"tools":   mcpTools(s.mcp, item.Name, file.MCPServers[item.Name]),
		}
		if len(item.Args) > 0 {
			row["args"] = item.Args
		}
		if item.URL != "" {
			row["url"] = item.URL
		}
		mc = append(mc, row)
	}
	return sk, mc
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	cat := s.registry.Models(s.requireModelCredential)
	out := make([]map[string]any, 0, len(cat))
	for _, m := range cat {
		out = append(out, map[string]any{
			"provider":           m.Provider,
			"id":                 m.ID,
			"name":               m.Name,
			"api":                m.API,
			"contextWindow":      m.ContextWindow,
			"maxTokens":          m.MaxTokens,
			"input":              m.Input,
			"applyPatchToolType": m.ApplyPatchToolType,
			"reasoning":          m.Reasoning,
			"thinkingLevels":     provider.SupportedThinkingLevels(m),
			"defaultThinking":    provider.DefaultThinking(m),
			"builtin":            m.Builtin,
			"customized":         m.Customized,
			"spec":               m.Provider + "/" + m.ID,
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) meta(w http.ResponseWriter, _ *http.Request) {
	home, _ := os.UserHomeDir()
	def := s.registry.Default()
	out := map[string]any{
		"home":     home,
		"provider": def.Provider,
		"model":    def.Model,
	}
	if _, model, ok := s.registry.FindModel(def.Provider, def.Model); ok {
		out["thinkingEffort"] = provider.DefaultThinking(model)
	}
	writeJSON(w, 200, out)
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request) {
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	var body struct {
		Model          string  `json:"model"`
		ThinkingEffort *string `json:"thinkingEffort"`
		Title          *string `json:"title"`
		Pinned         *bool   `json:"pinned"`
		LeafID         *string `json:"leafId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Model != "" || body.ThinkingEffort != nil {
		spec := body.Model
		if spec == "" {
			spec = sess.Config.Model
		}
		ref, model, err := s.registry.ResolveSpec(spec, sess.Config.Provider)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		effort := sess.Config.ThinkingEffort
		if body.ThinkingEffort != nil {
			effort = *body.ThinkingEffort
		}
		effort, err = resolveThinking(model, effort)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if err := sess.SetModelAndThinking(ref.Provider, ref.Model, effort); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.rememberModel(ref)
	}
	if body.LeafID != nil {
		if s.running(sess.ID()) {
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
		if err := sess.SetLeaf(*body.LeafID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if body.Title != nil {
		if err := sess.SetTitle(*body.Title); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if body.Pinned != nil {
		if err := sess.SetPinned(*body.Pinned); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rec, ok := s.ws.Match(sess.Header.CWD); ok && *body.Pinned {
			first := ""
			if len(rec.SessionIDs) > 0 {
				first = rec.SessionIDs[0]
			}
			if first != sess.ID() {
				if first == "" {
					_ = s.ws.AttachSession(rec.ID, sess.ID())
				} else {
					_ = s.ws.AttachSession(rec.ID, sess.ID())
					_ = s.ws.InsertSessionBefore(rec.ID, sess.ID(), first)
				}
			}
		}
	}
	writeJSON(w, 200, s.sessionMap(sess, nil))
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	infos, err := session.List(s.cfg.Sessions.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		out = append(out, s.infoMap(info))
	}
	writeJSON(w, 200, out)
}

func (s *Server) running(id string) bool {
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st == nil {
		return false
	}
	select {
	case <-st.done:
		return false
	default:
		return true
	}
}

func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Text     string          `json:"text"`
		Content  []types.Content `json:"content"`
		ParentID *string         `json:"parentId"`
		Model    string          `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(body.Content) == 0 && strings.TrimSpace(body.Text) != "" {
		body.Content = []types.Content{{Type: "text", Text: body.Text}}
	}
	if err := validateUserContent(body.Content); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parsed := command.Parse(contentText(body.Content))
	sess, err := s.open(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	parsed = command.ResolveUnknown(parsed, snapshot)
	tg := toggles.Load(s.cfg.Home)
	busy := s.running(id)
	if parsed.Kind != command.KindPrompt {
		if hasNonText(body.Content) {
			_ = sess.Close()
			http.Error(w, "commands do not take attachments", http.StatusBadRequest)
			return
		}
		if parsed.Args != "" && parsed.Kind == command.KindBuiltin {
			_ = sess.Close()
			writeHandled(w, "usage: /"+parsed.Name, true)
			return
		}
		if busy && !command.AllowBusy(parsed) {
			_ = sess.Close()
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
		switch parsed.Kind {
		case command.KindBuiltin:
			_ = sess.Close()
			s.handleBuiltin(w, r, parsed.Name)
			return
		case command.KindSkill:
			text, ok := command.ExpandSkill(snapshot, tg.Skills, parsed.Name, parsed.Args)
			if !ok {
				_ = sess.Close()
				writeHandled(w, "unknown skill /skill:"+parsed.Name, true)
				return
			}
			body.Content = []types.Content{{Type: "text", Text: text}}
		case command.KindTemplate:
			text, ok := command.ExpandTemplate(snapshot, parsed.Name, parsed.Args)
			if !ok {
				_ = sess.Close()
				writeHandled(w, "unknown command /"+parsed.Name, true)
				return
			}
			body.Content = []types.Content{{Type: "text", Text: text}}
		default:
			_ = sess.Close()
			writeHandled(w, "unknown command /"+parsed.Name, true)
			return
		}
	} else if busy {
		_ = sess.Close()
		http.Error(w, "session busy", http.StatusConflict)
		return
	}
	if body.ParentID != nil && *body.ParentID != "" {
		messages, err := sess.MessagesTo(*body.ParentID)
		if err != nil {
			_ = sess.Close()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if hasUnresolvedToolCalls(messages) {
			_ = sess.Close()
			http.Error(w, "parent leaves unresolved tool calls", http.StatusBadRequest)
			return
		}
	}
	spec := body.Model
	if spec == "" {
		spec = sess.Config.Model
	}
	ref, selectedModel, err := s.registry.ResolveSpec(spec, sess.Config.Provider)
	if err == nil && s.requireModelCredential {
		_, selectedModel, _, err = s.registry.Resolve(ref.Provider, ref.Model)
	}
	if err == nil && !slices.Contains(selectedModel.Input, "image") {
		for _, c := range body.Content {
			if c.Type == "image" {
				err = errors.New("selected model does not support image input")
				break
			}
		}
	}
	_ = sess.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.startRun(w, id, body.Content, body.ParentID, body.Model)
}

func (s *Server) handleBuiltin(w http.ResponseWriter, r *http.Request, name string) {
	switch name {
	case "reload":
		s.Reload()
		writeHandled(w, "reloaded resources and MCP connections", false)
	case "compact":
		s.doCompact(w, r)
	default:
		writeHandled(w, "unknown command /"+name, true)
	}
}

func validateUserContent(content []types.Content) error {
	const maxInlineImage = 20 << 20
	usable := false
	for i := range content {
		c := &content[i]
		switch c.Type {
		case "", "text":
			if strings.TrimSpace(c.Text) != "" {
				usable = true
			}
		case "image", "file", "workspace_file":
			if c.Path == "" && c.Data == "" {
				return fmt.Errorf("content[%d]: attachment path required", i)
			}
			if c.Path != "" {
				abs, err := filepath.Abs(c.Path)
				if err != nil {
					return fmt.Errorf("content[%d]: invalid attachment path", i)
				}
				// The WebUI intentionally selects host files through the authenticated
				// server-side browser; re-stat the normalized absolute path so a stale
				// or directory selection cannot enter persisted model context.
				st, err := os.Stat(abs)
				if err != nil || !st.Mode().IsRegular() {
					return fmt.Errorf("content[%d]: attachment unreadable", i)
				}
				c.Path = abs
				c.Size = st.Size()
				// Images are materialized into base64 for provider requests. Bound the
				// read here so a selected disk image cannot multiply into an unbounded
				// request allocation; ordinary file references are never inlined.
				if c.Type == "image" && st.Size() > maxInlineImage {
					return fmt.Errorf("content[%d]: image exceeds 20 MiB", i)
				}
				if c.Name == "" {
					c.Name = filepath.Base(abs)
				}
			}
			usable = true
		default:
			return fmt.Errorf("content[%d]: unsupported type %q", i, c.Type)
		}
	}
	if !usable {
		return errors.New("text or attachment required")
	}
	return nil
}

func materializeAttachments(_ context.Context, messages []types.Message) ([]types.Message, error) {
	out := make([]types.Message, len(messages))
	for i, m := range messages {
		out[i] = m
		out[i].Content = make([]types.Content, 0, len(m.Content))
		for _, c := range m.Content {
			switch c.Type {
			case "file", "workspace_file":
				label := c.Name
				if label == "" {
					label = filepath.Base(c.Path)
				}
				out[i].Content = append(out[i].Content, types.Content{Type: "text", Text: fmt.Sprintf("\nAttached file %q is available at: %s", label, c.Path)})
			case "image":
				if c.Data == "" && c.Path != "" {
					b, err := os.ReadFile(c.Path) //nolint:gosec // authenticated host file selected by the user
					if err != nil {
						return nil, fmt.Errorf("read attachment %q: %w", c.Path, err)
					}
					c.Data = base64.StdEncoding.EncodeToString(b)
					if c.MIMEType == "" {
						c.MIMEType = http.DetectContentType(b)
					}
				}
				out[i].Content = append(out[i].Content, c)
			default:
				out[i].Content = append(out[i].Content, c)
			}
		}
	}
	return out, nil
}

func hasUnresolvedToolCalls(messages []types.Message) bool {
	pending := map[string]bool{}
	for _, m := range messages {
		for _, call := range m.ToolCalls() {
			if call.ID != "" {
				pending[call.ID] = true
			}
		}
		if m.Role == "toolResult" && m.ToolCallID != "" {
			delete(pending, m.ToolCallID)
		}
	}
	return len(pending) != 0
}

func (s *Server) runPrompt(ctx context.Context, st *runState, id string, content []types.Content, parentID *string, reqModel string) {
	defer func() {
		// A prompt panic must still close done, otherwise SSE clients stay busy
		// forever waiting for a run that can no longer produce events.
		logging.Recover("prompt panic", "session_id", id)
		close(st.done)
		st.mu.Lock()
		if st.wait != nil {
			st.wait.Broadcast()
		}
		st.mu.Unlock()
	}()
	sess, err := s.open(id)
	if err != nil {
		st.err = err
		return
	}
	defer func() { _ = sess.Close() }()
	if parentID != nil {
		if err := sess.SetLeaf(*parentID); err != nil {
			st.err = err
			return
		}
	}
	cfg := s.cfg
	if reqModel != "" {
		ref, nextModel, resolveErr := s.registry.ResolveSpec(reqModel, sess.Config.Provider)
		if resolveErr != nil {
			st.err = resolveErr
			return
		}
		effort, resolveErr := resolveThinking(nextModel, sess.Config.ThinkingEffort)
		if resolveErr != nil {
			st.err = resolveErr
			return
		}
		if ref.Provider != sess.Config.Provider || ref.Model != sess.Config.Model || effort != sess.Config.ThinkingEffort {
			if err := sess.SetModelAndThinking(ref.Provider, ref.Model, effort); err != nil {
				slog.Warn("set model", "session_id", id, "provider", ref.Provider, "model", ref.Model, "err", err)
			}
		}
	}
	if parentID != nil {
		// A per-request model change is metadata, not the parent of an edited
		// user message. Re-select the requested base so user alternatives remain
		// true siblings and branch navigation does not depend on model settings.
		if err := sess.SetLeaf(*parentID); err != nil {
			st.err = err
			return
		}
	}
	_, info, ok := s.registry.FindModel(sess.Config.Provider, sess.Config.Model)
	if !ok {
		st.err = fmt.Errorf("model %q/%q is unavailable", sess.Config.Provider, sess.Config.Model)
		return
	}
	s.rememberModel(provider.ModelRef{Provider: sess.Config.Provider, Model: sess.Config.Model})
	runStreamer := s.streamer
	if s.requireModelCredential {
		_, resolved, key, resolveErr := s.registry.Resolve(sess.Config.Provider, sess.Config.Model)
		if resolveErr != nil {
			st.err = resolveErr
			return
		}
		info = resolved
		runStreamer = provider.NewLiveModel(resolved, key, nil)
	}
	jobs := s.jobsFor(id)
	profile := tools.Profile{
		RichRead: slices.Contains(info.Input, "image"),
		Editor:   tools.EditorWriteEdit,
	}
	if info.ApplyPatchToolType == "freeform" {
		profile.Editor = tools.EditorApplyPatch
	}
	tls := tools.Set{CWD: sess.Header.CWD, Jobs: jobs, Shells: s.shells, Mutations: s.mutations}.Build(profile)
	tg := toggles.Load(cfg.Home)
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	mcpFile := snapshot.MCP
	// Bind is cache-only. Waiting here for mcp.Tools() spawn used to leave the
	// UI on running with no SSE until every enabled server finished handshake.
	tls = append(tls, s.mcp.Bind(mcpFile, tg.MCP)...)
	go func() {
		defer logging.Recover("mcp prefetch panic", "session_id", id)
		ctx, cancel := context.WithTimeout(ctx, mcp.HandshakeTimeout)
		defer cancel()
		s.mcp.Prefetch(ctx, mcpFile, tg.MCP)
	}()
	// The snapshot is fixed for the session until reload, while the prompt is
	// rendered per request so tool schemas and runtime metadata stay current.
	sys := prompt.Build(prompt.Input{Resources: snapshot, Tools: tls, Toggle: tg.Skills})

	var emit func(loop.Event) error
	emit = func(ev loop.Event) error {
		if ev.Type == loop.MessageEnd && ev.Message != nil {
			e, err := sess.AppendMessage(*ev.Message)
			if err != nil {
				return fmt.Errorf("append message: %w", err)
			}
			ev.EntryID = e.ID
		}
		if ev.Type == loop.RequestHeader {
			tools := make([]session.ToolSchema, 0, len(ev.Tools))
			for _, t := range ev.Tools {
				var format *session.ToolFormat
				if t.Format != nil {
					format = &session.ToolFormat{Type: t.Format.Type, Syntax: t.Format.Syntax, Definition: t.Format.Definition}
				}
				tools = append(tools, session.ToolSchema{Type: t.Type, Name: t.Name, Description: t.Description, Parameters: t.Parameters, Format: format})
			}
			if _, err := sess.AppendRequestHeader(ev.System, tools, session.RequestMeta{Provider: sess.Config.Provider, Model: sess.Config.Model, ThinkingEffort: sess.Config.ThinkingEffort, CatalogVersion: provider.CatalogVersion, Pricing: info.Cost}); err != nil {
				return fmt.Errorf("append request header: %w", err)
			}
		}
		// Compaction progress is persisted too (decision: jsonl + SSE) so a
		// session replay shows when compaction happened.
		if ev.Type == loop.CompactionStart || ev.Type == loop.CompactionEnd {
			if _, err := sess.AppendEvent(string(ev.Type), ev.Reason, ev.OK); err != nil {
				return fmt.Errorf("append loop event: %w", err)
			}
		}
		if ev.Type == loop.ToolExecutionUpdate {
			progress, _ := json.Marshal(ev.PartialResult)
			if _, err := sess.AppendEvent(string(ev.Type), string(progress), true); err != nil {
				return fmt.Errorf("append tool progress: %w", err)
			}
		}
		st.mu.Lock()
		st.evs = append(st.evs, ev)
		st.wait.Broadcast()
		st.mu.Unlock()
		if ev.Type == loop.RequestHeader || (ev.Type == loop.MessageEnd && ev.Message != nil && ev.Message.Role == "assistant") {
			messages := sess.MessagesToLeaf()
			used := compact.EstimateTokens(messages, sess.LastCompactionAt())
			if ev.Type == loop.RequestHeader && !hasUsableContextUsage(messages, sess.LastCompactionAt()) {
				toolJSON, _ := json.Marshal(ev.Tools)
				used += (len(ev.System) + len(toolJSON) + 3) / 4
			}
			window := info.ContextWindow
			if cap := cfg.Compaction.MaxContextTokens; cap > 0 && cap < window {
				window = cap
			}
			estimated := ev.Type == loop.RequestHeader || ev.Message == nil || !usableUsage(ev.Message.Usage)
			if _, err := sess.AppendContextUsage(used, window, estimated); err != nil {
				return fmt.Errorf("append context usage: %w", err)
			}
			contextEvent := loop.Event{Type: loop.ContextUsage, Provider: sess.Config.Provider, Model: sess.Config.Model, CatalogVersion: provider.CatalogVersion, UsedTokens: used, ContextWindow: window, Estimated: estimated}
			st.mu.Lock()
			st.evs = append(st.evs, contextEvent)
			st.wait.Broadcast()
			st.mu.Unlock()
		}
		if ev.Type == loop.AgentEnd {
			// agent_end 后：threshold 检查——上下文超窗口就压缩。
			if s.shouldCompact(sess, info.ContextWindow) {
				_ = emit(loop.Event{Type: loop.CompactionStart, Reason: "threshold"})
				if _, err := s.compactSession(ctx, sess); err != nil && !errors.Is(err, compact.ErrNothingToCompact) {
					slog.Warn("auto compact", "session_id", id, "err", err)
					_ = emit(loop.Event{Type: loop.CompactionEnd, Reason: "threshold", OK: false})
				} else {
					_ = emit(loop.Event{Type: loop.CompactionEnd, Reason: "threshold", OK: true})
				}
			}
		}
		return nil
	}

	// 运行前 preflight：resume 会话或超大 prompt 可能已超窗口，loop.Run
	// 之前先压缩一次。不阻断：压缩失败只 warn。
	if s.shouldCompact(sess, info.ContextWindow) {
		_ = emit(loop.Event{Type: loop.CompactionStart, Reason: "preflight"})
		if _, err := s.compactSession(ctx, sess); err != nil && !errors.Is(err, compact.ErrNothingToCompact) {
			slog.Warn("preflight compact", "session_id", id, "err", err)
			_ = emit(loop.Event{Type: loop.CompactionEnd, Reason: "preflight", OK: false})
		} else {
			_ = emit(loop.Event{Type: loop.CompactionEnd, Reason: "preflight", OK: true})
		}
	}

	_, err = loop.RunMessage(ctx, types.Message{Role: "user", Content: content}, sess.MessagesToLeaf(), loop.Config{
		Streamer:                runStreamer,
		Tools:                   tls,
		System:                  sys,
		Provider:                sess.Config.Provider,
		Model:                   sess.Config.Model,
		API:                     info.API,
		MaxTokens:               info.MaxTokens,
		ThinkingEffort:          sess.Config.ThinkingEffort,
		ThinkingFormat:          info.Compat.ThinkingFormat,
		MaxTokensField:          info.Compat.MaxTokensField,
		SupportsReasoningEffort: info.Compat.SupportsReasoningEffort,
		ForceAdaptiveThinking:   info.Compat.ForceAdaptiveThinking,
		ThinkingLevelMap:        info.ThinkingLevelMap,
		TextOnly:                !slices.Contains(info.Input, "image"),
		MaxRetries:              5,
		BaseDelay:               2 * time.Second,
		Parallel:                true,
		Hooks: loop.Hooks{
			TransformContext: materializeAttachments,
			// 溢出恢复：请求报上下文溢出时，同 Run 内压缩后重试（事件不重放）。
			// 失败的 assistant 留在 history，但 provider replayable 会在重试时
			// 丢弃 stopReason=error 的轮次。
			OnContextOverflow: func(ctx context.Context) ([]types.Message, error) {
				return s.compactSession(ctx, sess)
			},
		},
	}, emit)
	st.err = err
}

func usableUsage(usage *types.Usage) bool {
	return usage != nil && (usage.TotalTokens > 0 || usage.Input+usage.Output+usage.CacheRead+usage.CacheWrite > 0)
}

func hasUsableContextUsage(messages []types.Message, after int64) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "assistant" {
			continue
		}
		return (after == 0 || m.Timestamp > after) && usableUsage(m.Usage)
	}
	return false
}

func (s *Server) summarizer(provider, model string) compact.Summarizer {
	return compact.StreamSummarizer{
		Stream: func(ctx context.Context, system, user string) (string, *types.Usage, error) {
			m, err := s.streamer.Stream(ctx, loop.Request{
				Provider: provider,
				Model:    model,
				System:   system,
				Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: user}}}},
			}, func(loop.AssistantDelta) error { return nil })
			if err != nil {
				return "", nil, fmt.Errorf("stream summarizer: %w", err)
			}
			return m.Text(), m.Usage, nil
		},
	}
}

func (s *Server) shouldCompact(sess *session.Session, window int) bool {
	msgs := sess.MessagesToLeaf()
	return compact.ShouldRun(compact.EstimateTokens(msgs, sess.LastCompactionAt()), window, s.cfg.Compaction)
}

// compactSession runs one compaction (Prepare + Execute + AppendCompaction)
// and returns the new model-facing context. Shared by preflight、溢出恢复和
// threshold 三条路径。Returns ErrNothingToCompact when the
// conversation fits inside the recent-token budget (no model call is made).
//
// A successful compaction invalidates every cached resource snapshot: the
// model context was rebuilt, so the next prompt should also use freshly loaded
// environment, instructions, skills, templates, and MCP configuration.
func (s *Server) compactSession(ctx context.Context, sess *session.Session) ([]types.Message, error) {
	prep, err := compact.Prepare(sess.LeafEntries(), s.cfg.Compaction)
	if err != nil {
		return nil, fmt.Errorf("prepare compaction: %w", err)
	}
	if prep == nil {
		return nil, compact.ErrNothingToCompact
	}
	summary, usage, err := compact.Execute(ctx, prep, s.summarizer(sess.Config.Provider, sess.Config.Model), s.cfg.Compaction)
	if err != nil {
		return nil, fmt.Errorf("execute compaction: %w", err)
	}
	if _, err := sess.AppendCompaction(summary, prep.FirstKeptEntryID, prep.TokensBefore, usage, prep.RetainedTail); err != nil {
		return nil, fmt.Errorf("append compaction: %w", err)
	}
	s.Reload()
	return sess.MessagesToLeaf(), nil
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache") // 防止代理/浏览器缓冲导致事件延迟到达
	if st == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	fl, _ := w.(http.Flusher)
	go func() {
		<-r.Context().Done()
		st.mu.Lock()
		st.wait.Broadcast()
		st.mu.Unlock()
	}() // 客户端断开不泄漏 goroutine
	idx := 0
	for {
		if r.Context().Err() != nil {
			return
		}
		st.mu.Lock()
		for idx >= len(st.evs) {
			select {
			case <-st.done:
				st.mu.Unlock()
				return
			case <-r.Context().Done():
				st.mu.Unlock()
				return
			default:
				st.wait.Wait()
			}
		}
		ev := st.evs[idx]
		idx++
		st.mu.Unlock()
		b, err := json.Marshal(ev)
		if err != nil {
			slog.Error("marshal SSE event", "err", err)
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
		if fl != nil {
			fl.Flush()
		}
		if ev.Type == loop.AgentEnd {
			return
		}
	}
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st != nil {
		st.cancel()
	}
	writeJSON(w, 200, map[string]any{"aborted": true})
}

func (s *Server) doCompact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.open(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	st, ctx, err := s.occupy(id, r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	e, err := compact.Run(ctx, sess, s.summarizer(sess.Config.Provider, sess.Config.Model), s.cfg.Compaction)
	s.release(id, st)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "compact aborted", http.StatusConflict)
		return
	}
	if err != nil {
		if errors.Is(err, compact.ErrNothingToCompact) {
			http.Error(w, "nothing to compact (session too small)", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Reload()
	writeJSON(w, 200, map[string]any{"id": e.ID, "type": e.Type, "firstKeptEntryId": e.FirstKeptEntryID, "handled": true})
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	if s.running(r.PathValue("id")) {
		// Each HTTP request opens its own Session and therefore has a different
		// mutex. Reject a live writer instead of copying a half-finished tool turn.
		http.Error(w, "session busy", http.StatusConflict)
		return
	}
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	var body struct {
		EntryID string `json:"entryId"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
	}
	dst, err := session.ForkAt(s.cfg.Sessions.Root, sess, body.EntryID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = dst.Close() }()
	if rec, ok := s.ws.Match(dst.Header.CWD); ok {
		_ = s.ws.AttachSession(rec.ID, dst.ID())
	}
	s.sidx.Add(dst.ID(), dst.Dir)
	writeJSON(w, 200, s.sessionMap(dst, map[string]any{"parentSession": dst.Header.ParentSession}))
}

func (s *Server) rememberModel(ref provider.ModelRef) {
	if err := s.registry.RememberDefault(ref); err != nil {
		slog.Warn("remember last model", "provider", ref.Provider, "model", ref.Model, "err", err)
	}
}

// resolveThinking uses the model's default when the client omits effort,
// otherwise clamps to the nearest supported level. Why: create/patch/prompt
// share this so an empty thinkingEffort never silently becomes "off" (the
// first supported level) and a kept effort survives a model switch.
func resolveThinking(model provider.Model, requested string) (string, error) {
	if requested == "" {
		return provider.DefaultThinking(model), nil
	}
	return provider.ClampThinking(model, requested)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write JSON response", "err", err)
	}
}
