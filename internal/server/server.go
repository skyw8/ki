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
	"ki/internal/extension"
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
	mcp                    *mcp.Manager
	ext                    *extension.Manager
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
	mcpSubscribers         map[string]map[chan loop.Event]struct{}
	pendingReload          map[string]bool
	extUI                  map[string]map[string]*extUIState
	pendingSettled         []settledEnqueue
	idempotency            map[string]extension.EnqueueResult
	activeTools            map[string][]string
	uiAnswers              map[string]chan uiAnswer
	runtimeMu              sync.Mutex
	runtime                map[string]*runtimePrep
}

type runState struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	evs    []loop.Event
	wait   *sync.Cond
	done   chan struct{}
	err    error
	inbox  *loop.Inbox
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
	srv := &Server{
		cfg:                    opt.Config,
		token:                  tok,
		streamer:               st,
		registry:               reg,
		requireModelCredential: requireCredential,
		resources:              resources.NewLoader(opt.Config.Home),
		runs:                   map[string]*runState{},
		jobs:                   map[string]*tools.JobStore{},
		ws:                     ws,
		sidx:                   sidx,
		shells:                 shells,
		mutations:              tools.NewMutationQueue(),
		mcpSubscribers:         map[string]map[chan loop.Event]struct{}{},
		pendingReload:          map[string]bool{},
		extUI:                  map[string]map[string]*extUIState{},
		idempotency:            map[string]extension.EnqueueResult{},
		activeTools:            map[string][]string{},
		uiAnswers:              map[string]chan uiAnswer{},
		runtime:                map[string]*runtimePrep{},
	}
	srv.mcp = mcp.NewManager(srv.onMCPNotification)
	srv.ext = extension.NewManager(opt.Config.Home, srv.onExtensionError)
	srv.ext.SetHost(srv)
	return srv, nil
}

func liveFromRegistry(reg *provider.Registry) loop.Streamer {
	return &router{registry: reg}
}

type router struct{ registry *provider.Registry }

func (r *router) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	_, m, key, err := r.registry.Resolve(req.Provider, req.Model)
	if err != nil {
		return types.Message{}, fmt.Errorf("resolve provider model: %w", err)
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
func (s *Server) Reload() bool {
	s.mu.Lock()
	active := make(map[string]bool, len(s.runs))
	for id, st := range s.runs {
		select {
		case <-st.done:
		default:
			active[id] = true
			s.pendingReload[id] = true
		}
	}
	s.mu.Unlock()
	s.resources.InvalidateAllExcept(active)
	if s.mcp != nil {
		s.mcp.CloseExcept(active)
	}
	if s.ext != nil {
		s.ext.CloseExcept(active)
	}
	s.resetRuntimeExcept(active)
	s.rewarmWatchers(active)
	return len(active) > 0
}

// reloadSession invalidates one idle session. A live run keeps its fixed tool
// header and connection; requestReload records pendingReload, and release
// applies it after occupy ends (prompt defer or compact).
func (s *Server) reloadSession(id string) {
	s.resources.Invalidate(id)
	if s.mcp != nil {
		s.mcp.CloseSession(id)
	}
	if s.ext != nil {
		s.ext.CloseSession(id)
	}
	s.resetRuntime(id)
	sess, err := s.open(id)
	if err != nil {
		return
	}
	cwd := sess.Header.CWD
	_ = sess.Close()
	s.kickWarmup(id, cwd)
}

func (s *Server) requestReload(id string) bool {
	s.mu.Lock()
	if st := s.runs[id]; st != nil {
		select {
		case <-st.done:
		default:
			s.pendingReload[id] = true
			s.mu.Unlock()
			return true
		}
	}
	s.mu.Unlock()
	s.reloadSession(id)
	return false
}

func (s *Server) onMCPNotification(sessionID string, notification mcp.Notification) {
	s.publishMCPEvent(sessionID, notification)
}

func (s *Server) publishMCPEvent(sessionID string, notification mcp.Notification) {
	typ := loop.MCPServerFailed
	status := mcp.StatusFailed
	if notification.Kind == "tools_changed" {
		typ = loop.MCPToolsChanged
		status = mcp.StatusStale
	}
	ev := loop.Event{
		Type: typ, Server: notification.Server, MessageText: notification.Message,
		ReloadRequired: true,
	}
	if dir, ok := s.sidx.Lookup(sessionID); ok {
		entry, err := session.AppendSidebandEvent(dir, string(typ), map[string]any{
			"server": notification.Server, "message": notification.Message, "reloadRequired": true,
		})
		if err != nil {
			slog.Warn("persist MCP event", "session_id", sessionID, "server", notification.Server, "err", err)
		} else {
			ev.EntryID = entry.ID
		}
	}
	s.resources.MarkMCPStatus(sessionID, notification.Server, status, notification.Message, ev.EntryID)
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
			// A slow browser must not block an SDK notification goroutine.
		}
	}
	s.mu.Unlock()
}

func (s *Server) subscribeMCP(sessionID string) (<-chan loop.Event, func()) {
	ch := make(chan loop.Event, 16)
	s.mu.Lock()
	if s.mcpSubscribers[sessionID] == nil {
		s.mcpSubscribers[sessionID] = map[chan loop.Event]struct{}{}
	}
	s.mcpSubscribers[sessionID][ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.mcpSubscribers[sessionID], ch)
		if len(s.mcpSubscribers[sessionID]) == 0 {
			delete(s.mcpSubscribers, sessionID)
		}
		s.mu.Unlock()
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
	api.HandleFunc("POST /v1/sessions/{id}/extension-ui", s.auth(s.extensionUIAnswer))
	api.HandleFunc("POST /v1/sessions/{id}/compact", s.auth(s.doCompact))
	api.HandleFunc("POST /v1/sessions/{id}/fork", s.auth(s.fork))
	api.HandleFunc("POST /v1/sessions/{id}/attachments", s.auth(s.uploadAttachment))
	api.HandleFunc("POST /v1/reload", s.auth(s.doReload))
	api.HandleFunc("GET /v1/skills", s.auth(s.getSkills))
	api.HandleFunc("PATCH /v1/skills", s.auth(s.patchSkills))
	api.HandleFunc("GET /v1/mcp", s.auth(s.getMCP))
	api.HandleFunc("PATCH /v1/mcp", s.auth(s.patchMCP))
	api.HandleFunc("GET /v1/extensions", s.auth(s.getExtensions))
	api.HandleFunc("PATCH /v1/extensions", s.auth(s.patchExtensions))
	api.HandleFunc("GET /v1/message", s.auth(s.getMessage))
	api.HandleFunc("PATCH /v1/message", s.auth(s.patchMessage))
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

// Shutdown stops the HTTP server and every session-scoped MCP client.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	jobs := make([]*tools.JobStore, 0, len(s.jobs))
	for id, store := range s.jobs {
		jobs = append(jobs, store)
		delete(s.jobs, id)
	}
	runs := make([]*runState, 0, len(s.runs))
	for _, st := range s.runs {
		if st != nil {
			st.cancel()
			runs = append(runs, st)
		}
	}
	s.mu.Unlock()
	for _, store := range jobs {
		store.Close()
	}
	for _, st := range runs {
		select {
		case <-st.done:
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	if s.ext != nil {
		s.ext.CloseExcept(nil)
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
	s.kickWarmup(sess.ID(), sess.Header.CWD)
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
	queued, err := session.ReadQueue(sess.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if queued == nil {
		queued = []session.QueuedItem{}
	}
	extQueued, err := session.ReadExtQueue(sess.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if extQueued == nil {
		extQueued = []session.ExtQueuedItem{}
	}
	s.kickWarmup(sess.ID(), sess.Header.CWD)
	// Why: ready is set after Prepare, so read ready first then RuntimeCommands
	// to avoid ready:true with an empty slash catalog.
	ready := s.runtimeReady(sess.ID())
	cmds := command.Catalog(snapshot, tg.Skills)
	if s.ext != nil {
		for _, spec := range s.ext.RuntimeCommands(sess.ID()) {
			cmds = append(cmds, command.Item{Name: spec.Name, Description: spec.Description, ArgumentHint: spec.ArgumentHint, Completions: spec.Completions, Source: "extension"})
		}
	}
	writeJSON(w, 200, s.sessionMap(sess, map[string]any{
		"leafId":              sess.LeafID(),
		"entries":             sess.Entries(),
		"messages":            sess.MessagesToLeaf(),
		"availableSkills":     sk,
		"availableMcp":        mc,
		"availableExtensions": s.extensionCatalog(snapshot),
		"commands":            cmds,
		"queued":              queued,
		"extQueued":           extQueued,
		"extensionUi":         s.extensionUIList(sess.ID()),
		"runtime":             map[string]any{"ready": ready},
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
		state, ok := snapshot.MCPServers[item.Name]
		if !ok {
			state.Status = mcp.StatusUnloaded
		}
		toolRows := make([]map[string]any, 0, len(state.Tools))
		for _, tool := range state.Tools {
			toolRows = append(toolRows, map[string]any{"name": tool.Name, "description": tool.Description})
		}
		row := map[string]any{
			"name":    item.Name,
			"command": item.Command,
			"source":  item.Source,
			"enabled": item.Enabled,
			"tools":   toolRows,
			"status":  state.Status,
		}
		if state.Error != "" {
			row["error"] = state.Error
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
		Model          string    `json:"model"`
		ThinkingEffort *string   `json:"thinkingEffort"`
		Title          *string   `json:"title"`
		Pinned         *bool     `json:"pinned"`
		LeafID         *string   `json:"leafId"`
		Queued         *[]string `json:"queued"`
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
	if body.Queued != nil {
		if _, err := session.KeepQueueIDs(sess.Dir, *body.Queued); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.publishQueueChanged(sess.ID())
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
		Delivery string          `json:"delivery"`
		QueueID  string          `json:"queueId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	queueID := strings.TrimSpace(body.QueueID)
	delivery := strings.TrimSpace(body.Delivery)
	if queueID != "" {
		if delivery == "" {
			delivery = toggles.BusySteer
		}
		if delivery != toggles.BusySteer {
			http.Error(w, errQueueIDRequiresSteer.Error(), http.StatusBadRequest)
			return
		}
		if body.ParentID != nil && *body.ParentID != "" {
			http.Error(w, errQueueIDWithParent.Error(), http.StatusBadRequest)
			return
		}
		if len(body.Content) > 0 || strings.TrimSpace(body.Text) != "" {
			http.Error(w, errQueueIDWithContent.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if len(body.Content) == 0 && strings.TrimSpace(body.Text) != "" {
			body.Content = []types.Content{{Type: "text", Text: body.Text}}
		}
		if err := validateUserContent(body.Content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	parsed := command.Parse(contentText(body.Content))
	sess, err := s.open(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	runtimeCmds := map[string]struct{}{}
	parsed = command.ResolveCommand(parsed, snapshot, runtimeCmds)
	if parsed.Kind == command.KindUnknown && s.ext != nil {
		tgPre := toggles.Load(s.cfg.Home)
		s.ext.Prepare(context.Background(), id, sess.Header.CWD, extension.Enabled(snapshot.Extensions, tgPre.Extensions)) // chain order via Enabled
		for name := range s.ext.RuntimeCommands(id) {
			runtimeCmds[name] = struct{}{}
		}
		parsed = command.ResolveCommand(command.Parse(contentText(body.Content)), snapshot, runtimeCmds)
	}
	tg := toggles.Load(s.cfg.Home)
	busy := s.running(id)
	live := s.runAt(id)
	if queueID == "" && parsed.Kind != command.KindPrompt {
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
		case command.KindExtension:
			if s.ext == nil {
				_ = sess.Close()
				writeHandled(w, "unknown command /"+parsed.Name, true)
				return
			}
			handled, notice, promptText, err := s.ext.InvokeCommand(r.Context(), id, parsed.Name, parsed.Args)
			if err != nil {
				_ = sess.Close()
				writeHandled(w, err.Error(), true)
				return
			}
			if handled {
				_ = sess.Close()
				writeHandled(w, notice, false)
				return
			}
			if strings.TrimSpace(promptText) == "" {
				_ = sess.Close()
				writeHandled(w, "unknown command /"+parsed.Name, true)
				return
			}
			body.Content = []types.Content{{Type: "text", Text: promptText}}
		case command.KindPrompt:
			// The outer condition normally excludes this case; keep the switch
			// exhaustive so a future parser change follows the normal prompt path.
		case command.KindUnknown:
			_ = sess.Close()
			writeHandled(w, "unknown command /"+parsed.Name, true)
			return
		}
	}
	if body.ParentID != nil && *body.ParentID != "" {
		if busy {
			_ = sess.Close()
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
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
	if queueID != "" {
		s.promoteQueued(w, r, id, sess, live, queueID, body.Model)
		return
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
				err = errSelectedModelNoImage
				break
			}
		}
	}
	if err != nil {
		_ = sess.Close()
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if delivery == "" {
		delivery = tg.Message.BusyDelivery()
	}
	if delivery != toggles.BusySteer && delivery != toggles.BusyQueue {
		_ = sess.Close()
		http.Error(w, "delivery must be steer or queue", http.StatusBadRequest)
		return
	}
	if busy {
		dir := sess.Dir
		_ = sess.Close()
		if delivery == toggles.BusySteer && s.pushSteerRun(live, body.Content) {
			writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "steered"})
			return
		}
		if _, err := session.Enqueue(dir, body.Content); err != nil {
			if errors.Is(err, session.ErrQueueFull) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.publishQueueChanged(id)
		writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "queued"})
		return
	}
	_ = sess.Close()
	if s.ext != nil {
		text := contentText(body.Content)
		next, swallow := s.ext.ApplyInput(r.Context(), id, text)
		if swallow {
			writeJSON(w, 200, map[string]any{"handled": true, "notice": "input swallowed by extension"})
			return
		}
		if next != text && next != "" {
			body.Content = []types.Content{{Type: "text", Text: next}}
		}
	}
	// The prompt is accepted asynchronously; detach it from the HTTP request
	// cancellation while retaining request-scoped values for downstream code.
	s.startRun(context.WithoutCancel(r.Context()), w, id, body.Content, body.ParentID, body.Model)
}

// promoteQueued takes a durable queue item and inserts it into the captured
// run. If that occupy has already ended, the item goes back to the head so
// dispatchQueue can start it; it is not steered into a replacement run.
func (s *Server) promoteQueued(w http.ResponseWriter, r *http.Request, id string, sess *session.Session, live *runState, queueID, model string) {
	dir := sess.Dir
	item, err := session.TakeQueueID(dir, queueID)
	if err != nil {
		_ = sess.Close()
		if errors.Is(err, session.ErrQueueItemNotFound) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	putBack := func() {
		if err := session.EnqueueFront(dir, item); err != nil {
			slog.Warn("requeue after failed promote", "session_id", id, "err", err)
			return
		}
		s.publishQueueChanged(id)
	}
	if err := validateUserContent(item.Content); err != nil {
		_ = sess.Close()
		putBack()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec := model
	if spec == "" {
		spec = sess.Config.Model
	}
	ref, selectedModel, err := s.registry.ResolveSpec(spec, sess.Config.Provider)
	if err == nil && s.requireModelCredential {
		_, selectedModel, _, err = s.registry.Resolve(ref.Provider, ref.Model)
	}
	if err == nil && !slices.Contains(selectedModel.Input, "image") {
		for _, c := range item.Content {
			if c.Type == "image" {
				err = errSelectedModelNoImage
				break
			}
		}
	}
	if err != nil {
		_ = sess.Close()
		putBack()
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	_ = sess.Close()
	if s.pushSteerRun(live, item.Content) {
		s.publishQueueChanged(id)
		writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "steered"})
		return
	}
	st, ctx, err := s.occupy(context.WithoutCancel(r.Context()), id)
	if err != nil {
		putBack()
		writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "queued"})
		return
	}
	st.inbox = &loop.Inbox{}
	go s.runPrompt(ctx, st, id, item.Content, nil, model, "", s.takeNextTurn(id))
	s.publishQueueChanged(id)
	writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "started"})
}

func (s *Server) handleBuiltin(w http.ResponseWriter, r *http.Request, name string) {
	switch name {
	case "reload":
		queued := s.requestReload(r.PathValue("id"))
		if queued {
			writeHandled(w, "reload queued until the current run finishes", false)
		} else {
			writeHandled(w, "reloaded session resources and MCP connections", false)
		}
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
				return fmt.Errorf("content[%d]: %w", i, errAttachmentPathRequired)
			}
			if c.Path != "" {
				abs, err := filepath.Abs(c.Path)
				if err != nil {
					return fmt.Errorf("content[%d]: %w", i, errAttachmentPathInvalid)
				}
				// The WebUI intentionally selects host files through the authenticated
				// server-side browser; re-stat the normalized absolute path so a stale
				// or directory selection cannot enter persisted model context.
				st, err := os.Stat(abs)
				if err != nil || !st.Mode().IsRegular() {
					return fmt.Errorf("content[%d]: %w", i, errAttachmentUnreadable)
				}
				c.Path = abs
				c.Size = st.Size()
				// Images are materialized into base64 for provider requests. Bound the
				// read here so a selected disk image cannot multiply into an unbounded
				// request allocation; ordinary file references are never inlined.
				if c.Type == "image" && st.Size() > maxInlineImage {
					return fmt.Errorf("content[%d]: %w", i, errImageTooLarge)
				}
				if c.Name == "" {
					c.Name = filepath.Base(abs)
				}
			}
			usable = true
		default:
			return fmt.Errorf("content[%d]: %w %q", i, errUnsupportedContent, c.Type)
		}
	}
	if !usable {
		return errTextOrAttachment
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
					b, err := os.ReadFile(c.Path)
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

func (s *Server) runPrompt(ctx context.Context, st *runState, id string, content []types.Content, parentID *string, reqModel, origin string, nextTurn []session.ExtQueuedItem) {
	defer func() {
		// Why: occupy must be paired with release. Closing done here used to
		// mark SSE idle without consuming pendingReload, so a /reload during
		// the prompt never invalidated the snapshot. release closes done (and
		// Broadcasts) itself; a second close panics.
		logging.Recover("prompt panic", "session_id", id)
		s.release(id, st)
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
		st.err = fmt.Errorf("model %q/%q is %w", sess.Config.Provider, sess.Config.Model, errModelUnavailable)
		return
	}
	s.rememberModel(provider.ModelRef{Provider: sess.Config.Provider, Model: sess.Config.Model})
	var liveKey string
	var liveModel provider.Model
	runStreamer := s.streamer
	if s.requireModelCredential {
		_, resolved, key, resolveErr := s.registry.Resolve(sess.Config.Provider, sess.Config.Model)
		if resolveErr != nil {
			st.err = resolveErr
			return
		}
		info = resolved
		liveModel = resolved
		liveKey = key
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
	// snapshot.Extensions is Discover.All (name-sorted catalog). Enabled
	// reorders to the intercept chain: global-by-name then project-by-name.
	extTools := s.ext.Prepare(context.Background(), sess.ID(), sess.Header.CWD, extension.Enabled(snapshot.Extensions, tg.Extensions))
	tls = append(tls, extTools...)
	prepared := s.mcp.Prepare(ctx, sess.ID(), snapshot.MCP, tg.MCP, snapshot.MCPServers)
	if ctx.Err() != nil {
		st.err = ctx.Err()
		return
	}
	if updated, ok := s.resources.UpdateMCP(sess.ID(), snapshot.Revision, prepared.States); ok {
		snapshot = updated
	}
	for name, state := range prepared.States {
		if state.Status == mcp.StatusFailed {
			s.publishMCPEvent(id, mcp.Notification{Kind: "server_failed", Server: name, Message: state.Error})
		}
	}
	tls = append(tls, prepared.Tools...)
	tls = s.filterActiveTools(id, tls)
	occ := s.ext.Occupy(id)
	if s.requireModelCredential {
		runStreamer = provider.NewLiveModel(liveModel, liveKey, occ.HTTPDoer())
		runStreamer = occ.WrapStreamer(runStreamer)
	}
	// The snapshot is fixed for the session until reload, while the prompt is
	// rendered per request so tool schemas and runtime metadata stay current.
	sys := prompt.Build(prompt.Input{Resources: snapshot, Tools: tls, Toggle: tg.Skills})

	var emit func(loop.Event) error
	emit = func(ev loop.Event) error {
		if ev.Type == loop.MessageEnd && ev.Message != nil {
			if s.ext != nil {
				rewritten := s.ext.ApplyMessageEnd(ctx, id, *ev.Message)
				ev.Message = &rewritten
			}
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
			progress, err := json.Marshal(ev.PartialResult)
			if err != nil {
				return fmt.Errorf("marshal tool progress: %w", err)
			}
			if _, err := sess.AppendEvent(string(ev.Type), string(progress), true); err != nil {
				return fmt.Errorf("append tool progress: %w", err)
			}
		}
		if ev.Type == loop.PatchApplyUpdated {
			details := map[string]any{"toolCallId": ev.ToolCallID, "toolName": ev.ToolName, "partialResult": ev.PartialResult}
			if _, err := sess.AppendDetailsEvent(string(ev.Type), details); err != nil {
				return fmt.Errorf("append patch preview: %w", err)
			}
		}
		st.mu.Lock()
		st.evs = append(st.evs, ev)
		st.wait.Broadcast()
		st.mu.Unlock()
		switch ev.Type {
		case loop.MessageUpdate, loop.ToolExecutionUpdate, loop.ContextUsage, loop.PatchApplyUpdated, loop.ExtensionError:
		default:
			go s.ext.OnEvent(id, extension.RedactEvent(ev, id))
		}
		if ev.Type == loop.RequestHeader || (ev.Type == loop.MessageEnd && ev.Message != nil && ev.Message.Role == "assistant") {
			messages := sess.MessagesToLeaf()
			used := compact.EstimateTokens(messages, sess.LastCompactionAt())
			if ev.Type == loop.RequestHeader && !hasUsableContextUsage(messages, sess.LastCompactionAt()) {
				toolJSON, err := json.Marshal(ev.Tools)
				if err != nil {
					return fmt.Errorf("marshal tool schemas: %w", err)
				}
				used += (len(ev.System) + len(toolJSON) + 3) / 4
			}
			window := info.ContextWindow
			if maxContext := cfg.Compaction.MaxContextTokens; maxContext > 0 && maxContext < window {
				window = maxContext
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
			// After agent_end, apply the threshold check and compact an oversized context.
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

	// Preflight before running: a resumed session or a very large prompt may already
	// exceed the context window, so compact once before loop.Run. This is non-blocking:
	// a compaction failure only emits a warning.
	if s.shouldCompact(sess, info.ContextWindow) {
		_ = emit(loop.Event{Type: loop.CompactionStart, Reason: "preflight"})
		if _, err := s.compactSession(ctx, sess); err != nil && !errors.Is(err, compact.ErrNothingToCompact) {
			slog.Warn("preflight compact", "session_id", id, "err", err)
			_ = emit(loop.Event{Type: loop.CompactionEnd, Reason: "preflight", OK: false})
		} else {
			_ = emit(loop.Event{Type: loop.CompactionEnd, Reason: "preflight", OK: true})
		}
	}

	hooks := s.composeHooks(sess, occ)
	if len(nextTurn) > 0 {
		hooks = s.withNextTurn(sess, st, hooks, nextTurn)
	}
	user := types.Message{Role: "user", Content: content, Origin: origin}
	_, err = loop.RunMessage(ctx, user, sess.MessagesToLeaf(), loop.Config{
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
		Inbox:                   st.inbox,
		Hooks:                   hooks,
	}, emit)
	st.err = err
}

func usableUsage(usage *types.Usage) bool {
	return usage != nil && (usage.TotalTokens > 0 || usage.Input+usage.Output+usage.CacheRead+usage.CacheWrite > 0)
}

func hasUsableContextUsage(messages []types.Message, after int64) bool {
	for _, m := range slices.Backward(messages) {
		if m.Role != "assistant" {
			continue
		}
		return (after == 0 || m.Timestamp > after) && usableUsage(m.Usage)
	}
	return false
}

func (s *Server) composeHooks(sess *session.Session, occ *extension.Occupy) loop.Hooks {
	var extHooks loop.Hooks
	if occ != nil {
		extHooks = occ.Hooks()
	}
	return loop.Hooks{
		BeforeRun: extHooks.BeforeRun,
		TransformContext: func(ctx context.Context, msgs []types.Message) ([]types.Message, error) {
			msgs, err := materializeAttachments(ctx, msgs)
			if err != nil {
				return nil, err
			}
			if extHooks.TransformContext == nil {
				return msgs, nil
			}
			return extHooks.TransformContext(ctx, msgs)
		},
		BeforeTool: extHooks.BeforeTool,
		AfterTool:  extHooks.AfterTool,
		OnContextOverflow: func(ctx context.Context) ([]types.Message, error) {
			return s.compactSession(ctx, sess)
		},
	}
}

func (s *Server) filterActiveTools(id string, tls []loop.Tool) []loop.Tool {
	s.mu.Lock()
	active := append([]string{}, s.activeTools[id]...)
	s.mu.Unlock()
	if len(active) == 0 {
		return tls
	}
	allow := make(map[string]bool, len(active))
	for _, n := range active {
		allow[n] = true
	}
	var out []loop.Tool
	for _, t := range tls {
		if allow[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) takeNextTurn(id string) []session.ExtQueuedItem {
	dir, ok := s.sidx.Lookup(id)
	if !ok {
		return nil
	}
	items, err := session.TakeNextTurn(dir)
	if err != nil || len(items) == 0 {
		return nil
	}
	s.publishQueueChanged(id)
	return items
}

// withNextTurn injects deliverAs=nextTurn items after the user message and
// before before_agent_start. Those items never start their own occupy.
func (s *Server) withNextTurn(sess *session.Session, st *runState, hooks loop.Hooks, items []session.ExtQueuedItem) loop.Hooks {
	inner := hooks.BeforeRun
	hooks.BeforeRun = func(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
		for _, item := range items {
			origin := ""
			if item.Extension != "" {
				origin = "extension:" + item.Extension
			}
			msg := types.Message{Role: "user", Content: item.Content, Origin: origin, Timestamp: time.Now().UnixMilli()}
			if e, aerr := sess.AppendMessage(msg); aerr == nil {
				ev := loop.Event{Type: loop.MessageEnd, Message: &msg, EntryID: e.ID}
				st.mu.Lock()
				st.evs = append(st.evs, ev)
				st.wait.Broadcast()
				st.mu.Unlock()
			}
			msgs = append(msgs, msg)
		}
		if inner != nil {
			return inner(ctx, system, msgs)
		}
		return system, msgs, nil
	}
	return hooks
}

func (s *Server) liveSummarizer(sessionID, prov, model string) loop.Streamer {
	if !s.requireModelCredential {
		return s.streamer
	}
	_, resolved, key, err := s.registry.Resolve(prov, model)
	if err != nil {
		return s.streamer
	}
	return provider.NewLiveModel(resolved, key, s.ext.HTTPDoer(sessionID))
}

func (s *Server) summarizer(sessionID, prov, model string) compact.Summarizer {
	stream := s.liveSummarizer(sessionID, prov, model)
	return compact.StreamSummarizer{
		Stream: func(ctx context.Context, system, user string) (string, *types.Usage, error) {
			m, err := stream.Stream(ctx, loop.Request{
				Provider: prov,
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
// and returns the new model-facing context. Shared by the preflight, overflow
// recovery, and threshold paths. Returns ErrNothingToCompact when the
// conversation fits inside the recent-token budget (no model call is made).
//
// A successful compaction requestReloads this session: the model context was
// rebuilt, so the next prompt should use freshly loaded environment,
// instructions, skills, templates, and MCP configuration.
func (s *Server) compactSession(ctx context.Context, sess *session.Session) ([]types.Message, error) {
	var customSummary string
	if s.ext != nil {
		ok, summary := s.ext.CompactAllowed(ctx, sess.ID())
		if !ok {
			return sess.MessagesToLeaf(), nil
		}
		customSummary = summary
	}
	prep, err := compact.Prepare(sess.LeafEntries(), s.cfg.Compaction)
	if err != nil {
		return nil, fmt.Errorf("prepare compaction: %w", err)
	}
	if prep == nil {
		return nil, compact.ErrNothingToCompact
	}
	summary := customSummary
	var usage *types.Usage
	if summary == "" {
		summary, usage, err = compact.Execute(ctx, prep, s.summarizer(sess.ID(), sess.Config.Provider, sess.Config.Model), s.cfg.Compaction)
		if err != nil {
			return nil, fmt.Errorf("execute compaction: %w", err)
		}
	}
	if _, err := sess.AppendCompaction(summary, prep.FirstKeptEntryID, prep.TokensBefore, usage, prep.RetainedTail); err != nil {
		return nil, fmt.Errorf("append compaction: %w", err)
	}
	// compactSession runs inside an occupied prompt, so this queues until
	// runPrompt's release instead of closing this turn's MCP connections.
	s.requestReload(sess.ID())
	return sess.MessagesToLeaf(), nil
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.URL.Query().Get("notifications") == "1" {
		s.mcpEvents(w, r, id)
		return
	}
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache") // Prevent proxy/browser buffering from delaying events.
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
	}() // Prevent a client disconnect from leaking the goroutine.
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

func (s *Server) mcpEvents(w http.ResponseWriter, r *http.Request, id string) {
	ch, unsubscribe := s.subscribeMCP(id)
	defer unsubscribe()
	sess, err := s.open(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	_ = sess.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	write := func(ev loop.Event) bool {
		b, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
		if fl != nil {
			fl.Flush()
		}
		return err == nil
	}
	for name, state := range snapshot.MCPServers {
		if state.Status != mcp.StatusFailed && state.Status != mcp.StatusStale {
			continue
		}
		typ := loop.MCPServerFailed
		if state.Status == mcp.StatusStale {
			typ = loop.MCPToolsChanged
		}
		if !write(loop.Event{Type: typ, EntryID: state.EventID, Server: name, MessageText: state.Error, ReloadRequired: true}) {
			return
		}
	}
	if s.runtimeReady(id) {
		if !write(loop.Event{Type: loop.RuntimeReady, OK: true}) {
			return
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !write(ev) {
				return
			}
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st != nil {
		s.publishRunAborted(id)
		st.cancel()
		if s.ext != nil {
			s.ext.CloseSession(id)
		}
	}
	s.cancelUIPrompts(id)
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
	st, ctx, err := s.occupy(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	e, err := compact.Run(ctx, sess, s.summarizer(sess.ID(), sess.Config.Provider, sess.Config.Model), s.cfg.Compaction)
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
	s.reloadSession(id)
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
		EntryID  string `json:"entryId"`
		ForkMode string `json:"forkMode"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
	}
	forkMode, err := session.NormalizeForkMode(body.ForkMode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dst, err := session.ForkAt(s.cfg.Sessions.Root, sess, body.EntryID, forkMode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = dst.Close() }()
	if rec, ok := s.ws.Match(dst.Header.CWD); ok {
		_ = s.ws.AttachSession(rec.ID, dst.ID())
	}
	s.sidx.Add(dst.ID(), dst.Dir)
	writeJSON(w, 200, s.sessionMap(dst, nil))
	s.kickWarmup(dst.ID(), dst.Header.CWD)
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
	effort, err := provider.ClampThinking(model, requested)
	if err != nil {
		return "", fmt.Errorf("clamp thinking effort: %w", err)
	}
	return effort, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write JSON response", "err", err)
	}
}
