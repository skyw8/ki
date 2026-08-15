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
	"ki/internal/tools"
	"ki/internal/types"
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
	mu       sync.Mutex
	runs     map[string]*runState
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
	return &Server{
		cfg:      opt.Config,
		token:    tok,
		streamer: st,
		runs:     map[string]*runState{},
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/sessions", s.auth(s.create))
	mux.HandleFunc("GET /v1/sessions/{id}", s.auth(s.get))
	mux.HandleFunc("POST /v1/sessions/{id}/prompt", s.auth(s.prompt))
	mux.HandleFunc("GET /v1/sessions/{id}/events", s.auth(s.events))
	mux.HandleFunc("POST /v1/sessions/{id}/abort", s.auth(s.abort))
	mux.HandleFunc("POST /v1/sessions/{id}/compact", s.auth(s.doCompact))
	mux.HandleFunc("POST /v1/sessions/{id}/fork", s.auth(s.fork))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
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

// Shutdown stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
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
		CWD   string `json:"cwd"`
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cwd := body.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cfg, _ := config.Load(cwd)
	// merge already-loaded home from s.cfg for tests
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
	sess, err := session.Create(root, cwd, p, m)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer sess.Close()
	writeJSON(w, 200, map[string]any{
		"id":       sess.ID(),
		"cwd":      sess.Header.CWD,
		"provider": sess.Config.Provider,
		"model":    sess.Config.Model,
		"dir":      sess.Dir,
	})
}

func (s *Server) open(id string) (*session.Session, error) {
	dir, err := session.Find(s.cfg.Sessions.Root, id)
	if err != nil {
		return nil, err
	}
	return session.Open(dir)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	defer sess.Close()
	writeJSON(w, 200, map[string]any{
		"id":       sess.ID(),
		"cwd":      sess.Header.CWD,
		"provider": sess.Config.Provider,
		"model":    sess.Config.Model,
		"leafId":   sess.LeafID(),
		"dir":      sess.Dir,
		"parent":   sess.Header.ParentSession,
	})
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
	tls = append(tls, mcp.Tools(ctx, mcpFile, sess.Config.MCP)...)
	sys, _ := prompt.Build(prompt.Input{Home: cfg.Home, CWD: sess.Header.CWD, Tools: tls, Toggle: sess.Config.Skills})

	emit := func(ev loop.Event) error {
		if ev.Type == loop.MessageEnd && ev.Message != nil {
			if _, err := sess.AppendMessage(*ev.Message); err != nil {
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
		Hooks: loop.Hooks{
			BeforeRun: func(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
				// Rebuild prefix each run (and after compact via maybeCompact + next request).
				sys2, _ := prompt.Build(prompt.Input{Home: cfg.Home, CWD: sess.Header.CWD, Tools: tls, Toggle: sess.Config.Skills})
				return sys2, msgs, nil
			},
		},
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
	w.Header().Set("Cache-Control", "no-cache")
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
	}()
	idx := 0
	for {
		if r.Context().Err() != nil {
			return
		}
		st.mu.Lock()
		for idx >= len(st.evs) {
			select {
			case <-st.done:
				if idx < len(st.evs) {
					continue
				}
				st.mu.Unlock()
				return
			case <-r.Context().Done():
				st.mu.Unlock()
				return
			default:
			}
			select {
			case <-st.done:
				if idx < len(st.evs) {
					continue
				}
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
	writeJSON(w, 200, map[string]any{
		"id":            dst.ID(),
		"parentSession": dst.Header.ParentSession,
		"dir":           dst.Dir,
		"provider":      dst.Config.Provider,
		"model":         dst.Config.Model,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
