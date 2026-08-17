package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ki/internal/compact"
	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/mcp"
	"ki/internal/prompt"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/skills"
	"ki/internal/tools"
	"ki/internal/types"
	"ki/internal/workspace"
)

// Options constructs a Server.
type Options struct {
	Config   config.Config
	Token    string
	Streamer loop.Streamer
}

// Server is the HTTP API.
type Server struct {
	cfg      config.Config
	token    string
	streamer loop.Streamer
	mcp      *mcp.Pool
	mu       sync.Mutex
	runs     map[string]*runState
	ws       *workspace.Store
	sidx     *session.Index
	ln       net.Listener
	http     *http.Server
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
func New(opt Options) *Server {
	tok := opt.Token
	if tok == "" {
		tok = newToken()
	}
	st := opt.Streamer
	if st == nil {
		st = liveFromConfig(opt.Config)
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
		cfg:      opt.Config,
		token:    tok,
		streamer: st,
		mcp:      mcp.NewPool(opt.Config.Home), // serve-wide; not per session / per prompt
		runs:     map[string]*runState{},
		ws:       ws,
		sidx:     sidx,
	}
}

func liveFromConfig(cfg config.Config) loop.Streamer {
	return &router{cfg: cfg}
}

type router struct{ cfg config.Config }

func (r *router) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	m := provider.Lookup(req.Provider, req.Model)
	p := r.cfg.Providers[req.Provider]
	base := p.BaseURL
	if base == "" {
		base = m.BaseURL
	}
	api := req.API
	if api == "" {
		api = m.API
	}
	return provider.NewLive(api, base, p.APIKey, nil).Stream(ctx, req, emit)
}

func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Token is the bearer secret.
func (s *Server) Token() string { return s.token }

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	api.HandleFunc("GET /v1/models", s.auth(s.models))
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
	api.HandleFunc("GET /v1/workspaces", s.auth(s.listWorkspaces))
	api.HandleFunc("POST /v1/workspaces", s.auth(s.createWorkspace))
	api.HandleFunc("PATCH /v1/workspaces/{id}", s.auth(s.patchWorkspace))
	api.HandleFunc("DELETE /v1/workspaces/{id}", s.auth(s.deleteWorkspace))
	api.HandleFunc("POST /v1/workspaces/{id}/move", s.auth(s.moveWorkspace))
	api.HandleFunc("POST /v1/workspaces/{id}/sessions/move", s.auth(s.moveWorkspaceSession))
	api.HandleFunc("GET /v1/fs", s.auth(s.listFS))
	api.HandleFunc("POST /v1/fs", s.auth(s.createFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			api.ServeHTTP(w, r)
			return
		}
		s.serveUI(w, r)
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
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.http = &http.Server{Handler: s.Handler()}
	if err := WriteServerFile(s.cfg.Home, File{Addr: ln.Addr().String(), Token: s.token}); err != nil {
		slog.Warn("server.json", "err", err)
	}
	slog.Info("listen", "addr", ln.Addr().String())
	return s.http.Serve(ln)
}

// Addr is the bound address.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown stops the HTTP server and MCP clients (pool children outlive a prompt).
func (s *Server) Shutdown(ctx context.Context) error {
	if s.mcp != nil {
		s.mcp.Close()
	}
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

// WriteServerFile persists addr+token.
func WriteServerFile(home string, f File) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
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
		CWD         string `json:"cwd"`
		WorkspaceID string `json:"workspaceId"`
		Model       string `json:"model"`
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
	cfg, _ := config.Load(rec.Path)
	if s.cfg.Home != "" {
		cfg.Home = s.cfg.Home
		if s.cfg.Sessions.Root != "" {
			cfg.Sessions.Root = s.cfg.Sessions.Root
		}
		if cfg.Providers == nil {
			cfg.Providers = s.cfg.Providers
		}
		if cfg.Defaults.Provider == "" {
			cfg.Defaults = s.cfg.Defaults
		}
	}
	p, m := provider.Resolve(cfg, "", "", body.Model)
	root := s.cfg.Sessions.Root
	if root == "" {
		root = cfg.Sessions.Root
	}
	sess, err := session.Create(root, rec.Path, p, m)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer sess.Close()
	_ = s.ws.AttachSession(rec.ID, sess.ID())
	s.sidx.Add(sess.ID(), sess.Dir)
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
		return nil, err
	}
	// Index miss (session created by another process, or stale): fall back to
	// a scan, then self-heal so the next lookup is O(1).
	dir, err := session.Find(root, id)
	if err != nil {
		return nil, err
	}
	s.sidx.Add(id, dir)
	return session.Open(dir)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	defer sess.Close()
	sk, mc := s.sessionCatalog(sess)
	writeJSON(w, 200, s.sessionMap(sess, map[string]any{
		"leafId":          sess.LeafID(),
		"entries":         sess.Entries(),
		"messages":        sess.MessagesToLeaf(),
		"skills":          sess.Config.Skills,
		"mcp":             sess.Config.MCP,
		"availableSkills": sk,
		"availableMcp":    mc,
	}))
}

func (s *Server) sessionCatalog(sess *session.Session) ([]map[string]any, []map[string]any) {
	sk := []map[string]any{}
	for _, item := range skills.List(s.cfg.Home, sess.Header.CWD) {
		sk = append(sk, map[string]any{
			"name":        item.Name,
			"description": item.Description,
			"path":        item.FilePath,
			"source":      item.Source,
			"enabled":     sess.Config.Skills.Allowed(item.Name),
		})
	}
	sort.Slice(sk, func(i, j int) bool {
		a, _ := sk[i]["name"].(string)
		b, _ := sk[j]["name"].(string)
		return a < b
	})
	mc := []map[string]any{}
	for _, item := range mcp.List(mcp.Load(s.cfg.Home, sess.Header.CWD), sess.Config.MCP) {
		row := map[string]any{
			"name":    item.Name,
			"command": item.Command,
			"source":  item.Source,
			"enabled": item.Enabled,
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

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	cat := provider.Catalog()
	out := make([]map[string]any, 0, len(cat))
	for _, m := range cat {
		out = append(out, map[string]any{
			"provider":      m.Provider,
			"id":            m.ID,
			"api":           m.API,
			"contextWindow": m.ContextWindow,
			"spec":          m.Provider + "/" + m.ID,
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	writeJSON(w, 200, map[string]any{
		"home":     home,
		"provider": s.cfg.Defaults.Provider,
		"model":    s.cfg.Defaults.Model,
	})
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request) {
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	defer sess.Close()
	var body struct {
		Model  string          `json:"model"`
		Title  *string         `json:"title"`
		Pinned *bool           `json:"pinned"`
		Skills *session.Toggle `json:"skills"`
		MCP    *session.Toggle `json:"mcp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	if body.Model != "" {
		p, m := provider.Resolve(s.cfg, sess.Config.Provider, sess.Config.Model, body.Model)
		if err := sess.SetModel(p, m); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	if body.Title != nil {
		if err := sess.SetTitle(*body.Title); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	if body.Pinned != nil {
		if err := sess.SetPinned(*body.Pinned); err != nil {
			http.Error(w, err.Error(), 500)
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
	if body.Skills != nil || body.MCP != nil {
		if err := sess.SetToggles(body.Skills, body.MCP); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	writeJSON(w, 200, s.sessionMap(sess, map[string]any{
		"skills": sess.Config.Skills,
		"mcp":    sess.Config.MCP,
	}))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	infos, err := session.List(s.cfg.Sessions.Root)
	if err != nil {
		http.Error(w, err.Error(), 500)
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
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "text required", 400)
		return
	}
	if _, err := s.open(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.mu.Lock()
	if st, busy := s.runs[id]; busy {
		select {
		case <-st.done:
			// previous run finished; replace
		default:
			s.mu.Unlock()
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &runState{cancel: cancel, done: make(chan struct{})}
	st.wait = sync.NewCond(&st.mu)
	s.runs[id] = st
	s.mu.Unlock()

	go s.runPrompt(ctx, st, id, body.Text, body.Model)
	writeJSON(w, 202, map[string]any{"session_id": id, "accepted": true})
}

func (s *Server) runPrompt(ctx context.Context, st *runState, id, text, reqModel string) {
	defer func() {
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
	defer sess.Close()
	cfg := s.cfg
	if reqModel != "" {
		p, m := provider.Resolve(cfg, sess.Config.Provider, sess.Config.Model, reqModel)
		if err := sess.SetModel(p, m); err != nil {
			slog.Warn("set model", "err", err)
		}
	}
	info := provider.Lookup(sess.Config.Provider, sess.Config.Model)
	jobs := tools.NewJobStore()
	tls := tools.Set{CWD: sess.Header.CWD, Jobs: jobs}.All()
	mcpFile := mcp.Load(cfg.Home, sess.Header.CWD)
	// Bind is cache-only. Waiting here for mcp.Tools() spawn used to leave the
	// UI on running with no SSE until every enabled server finished handshake.
	tls = append(tls, s.mcp.Bind(mcpFile, sess.Config.MCP)...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), mcp.HandshakeTimeout)
		defer cancel()
		s.mcp.Prefetch(ctx, mcpFile, sess.Config.MCP)
	}()
	// One Build: After compact the *next* prompt rebuilds. A BeforeRun rebuild
	// only repeated Discover on the same turn.
	sys, _ := prompt.Build(prompt.Input{Home: cfg.Home, CWD: sess.Header.CWD, Tools: tls, Toggle: sess.Config.Skills})

	emit := func(ev loop.Event) error {
		if ev.Type == loop.MessageEnd && ev.Message != nil {
			if _, err := sess.AppendMessage(*ev.Message); err != nil {
				return err
			}
		}
		if ev.Type == loop.RequestHeader {
			tools := make([]session.ToolSchema, 0, len(ev.Tools))
			for _, t := range ev.Tools {
				tools = append(tools, session.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
			}
			if _, err := sess.AppendRequestHeader(ev.System, tools); err != nil {
				return err
			}
		}
		st.mu.Lock()
		st.evs = append(st.evs, ev)
		st.wait.Broadcast()
		st.mu.Unlock()
		if ev.Type == loop.AgentEnd {
			s.maybeCompact(ctx, sess, info.ContextWindow)
		}
		return nil
	}
	_, err = loop.Run(ctx, text, sess.MessagesToLeaf(), loop.Config{
		Streamer:   s.streamer,
		Tools:      tls,
		System:     sys,
		Provider:   sess.Config.Provider,
		Model:      sess.Config.Model,
		API:        info.API,
		MaxRetries: 5,
		BaseDelay:  2 * time.Second,
		Parallel:   true,
	}, emit)
	st.err = err
}

func (s *Server) summarizer() compact.Summarizer {
	return compact.StreamSummarizer{
		Stream: func(ctx context.Context, system, user string) (string, *types.Usage, error) {
			m, err := s.streamer.Stream(ctx, loop.Request{
				System:   system,
				Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: user}}}},
			}, func(loop.AssistantDelta) error { return nil })
			if err != nil {
				return "", nil, err
			}
			return m.Text(), m.Usage, nil
		},
	}
}

func (s *Server) maybeCompact(ctx context.Context, sess *session.Session, window int) {
	msgs := sess.MessagesToLeaf()
	tok := compact.EstimateTokens(msgs)
	if !compact.ShouldRun(tok, window, s.cfg.Compaction) {
		return
	}
	_, err := compact.Run(ctx, sess, s.summarizer(), s.cfg.Compaction)
	if err != nil {
		slog.Warn("auto compact", "err", err)
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache") // 防止代理/浏览器缓冲导致事件延迟到达
	if st == nil {
		w.WriteHeader(200)
		return
	}
	fl, _ := w.(http.Flusher)
	go func() {
		<-r.Context().Done()
		st.mu.Lock()
		st.wait.Broadcast()
		st.mu.Unlock()
	}() //客户端断开不泄漏 goroutine
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
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
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
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	defer sess.Close()
	e, err := compact.Run(r.Context(), sess, s.summarizer(), s.cfg.Compaction)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]any{"id": e.ID, "type": e.Type, "firstKeptEntryId": e.FirstKeptEntryID})
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	defer sess.Close()
	dst, err := session.Fork(s.cfg.Sessions.Root, sess)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer dst.Close()
	if rec, ok := s.ws.Match(dst.Header.CWD); ok {
		_ = s.ws.AttachSession(rec.ID, dst.ID())
	}
	s.sidx.Add(dst.ID(), dst.Dir)
	writeJSON(w, 200, s.sessionMap(dst, map[string]any{"parentSession": dst.Header.ParentSession}))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
