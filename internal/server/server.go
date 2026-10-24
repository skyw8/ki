package server

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"ki/internal/agent"
	"ki/internal/buildinfo"
	"ki/internal/codemode"
	"ki/internal/command"
	"ki/internal/compact"
	"ki/internal/config"
	"ki/internal/extension"
	"ki/internal/idgen"
	"ki/internal/logging"
	"ki/internal/loop"
	mcpruntime "ki/internal/mcp"
	"ki/internal/prompt"
	"ki/internal/provider"
	"ki/internal/push"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/telemetry"
	"ki/internal/toggles"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin"
	codetools "ki/internal/tool/builtin/code"
	"ki/internal/tool/discovery"
	"ki/internal/tool/output"
	"ki/internal/types"
	"ki/internal/workspace"

	"ki/internal/process"
	filetools "ki/internal/tool/builtin/file"
)

type Options struct {
	Config         config.Config
	Token          string
	Streamer       loop.Streamer
	Registry       *provider.Registry
	CodeModeConfig *codemode.Config
}

type Server struct {
	cfg                    config.Config
	token                  string
	serverID               string
	pid                    int
	exe                    string
	startedAt              time.Time
	stopOnce               sync.Once
	stopReq                chan struct{}
	browserSessionCookie   string
	browserCSRFCookie      string
	streamer               loop.Streamer
	registry               *provider.Registry
	providerExtensions     *extension.ProviderManager
	requireModelCredential bool
	providerAuthMu         sync.Mutex
	providerAuth           map[string]*providerAuthState
	ext                    *extension.Manager
	extInstall             *extensionInstaller
	resources              *resources.Loader
	mu                     sync.Mutex
	runs                   map[string]*runState
	replay                 replayCache
	processes              map[string]*process.Manager
	closedProcesses        *process.Manager
	outputStore            *output.Store
	codeMode               *codemode.Manager
	mcpMu                  sync.Mutex
	mcpManagers            map[string]*mcpManagerEntry
	mcpClosed              bool
	mcpClock               uint64
	agentTasks             *agent.Controller
	ws                     *workspace.Store
	sidx                   *session.Index
	slist                  *session.ListCache
	ln                     net.Listener
	http                   *http.Server
	shells                 process.ShellRuntime
	mutations              *filetools.MutationQueue
	gsMu                   sync.Mutex
	gsubs                  map[*pushSub]struct{}
	pendingReload          map[string]bool
	extUI                  map[string]map[string]*extUIState
	globalExtUI            map[string]*extUIState
	pendingSettled         []settledEnqueue
	idempotency            map[string]extension.EnqueueResult
	inputGates             map[string]*sync.Mutex
	activeTools            map[string][]string
	uiAnswers              map[string]chan uiAnswer
	browserSessions        map[string]time.Time
	push                   *push.Service
	pushAbortMu            sync.Mutex
	pushAborted            map[string]struct{}
	toggleMu               sync.Mutex
	runtimeMu              sync.Mutex
	runtime                map[string]*runtimePrep
	runtimeCtx             context.Context
	runtimeCancel          context.CancelFunc
	runtimeWG              sync.WaitGroup
	sessionWG              map[string]*sync.WaitGroup
	runtimeClosed          bool
	shutdownDone           chan struct{}
	shutdownErr            error
	deletionMu             sync.Mutex
	deleting               map[string]bool
	stopping               map[string]int
	creationMu             sync.Mutex
	deletingWorkspaces     map[string]bool
}

const (
	browserSessionTTL = 12 * time.Hour
)

type authSource uint8

const (
	authNone authSource = iota
	authBearer
	authBrowserSession
)

type runState struct {
	agentTaskID         string
	agentGeneration     uint64
	inputMetadata       types.Message
	cancel              context.CancelFunc
	runID               string
	external            map[string]string
	cancelReason        string
	cancelSource        string
	mu                  sync.Mutex
	evs                 []*loop.Event
	wait                *sync.Cond
	done                chan struct{}
	released            chan struct{}
	err                 error
	inbox               *loop.Inbox
	questionSubmissions map[string]bool
	steerClosed         bool
	seq                 int64
	promptKey           string
	pending             []int
	partial             int
	readers             map[*runReader]struct{}
}

type runReader struct{ pos int }

var blankEvent = &loop.Event{Blank: true}

func (st *runState) minReaderPos() int {
	min := len(st.evs)
	for r := range st.readers {
		if r.pos < min {
			min = r.pos
		}
	}
	return min
}

func (st *runState) addReader(r *runReader) {
	if st.readers == nil {
		st.readers = map[*runReader]struct{}{}
	}
	st.readers[r] = struct{}{}
}

func (st *runState) removeReader(r *runReader) { delete(st.readers, r) }

func transient(t loop.EventType) bool {
	return t == loop.MessageStart || t == loop.MessageUpdate || t == loop.ToolExecutionUpdate || t == loop.PatchApplyUpdated
}

func (st *runState) appendLocked(ev *loop.Event) {
	ev.RunID = st.runID
	if ev.External == nil {
		ev.External = cloneExternal(st.external)
	}
	st.seq++
	ev.Seq = st.seq
	ev.BufferedAt = time.Now()
	ev.BufferedBytes = 0
	ev.BufferedBytes = eventPayloadBytes(ev)
	stored := *ev
	if ev.Type == loop.RequestHeader {
		key := promptDigest(ev.System, ev.Tools)
		if key == st.promptKey {
			stored.System = ""
			stored.Tools = nil
			stored.PromptUnchanged = true
		} else {
			st.promptKey = key
		}
	}
	idx := len(st.evs)
	st.evs = append(st.evs, &stored)
	switch {
	case transient(ev.Type):
		st.retirePartialLocked()
		st.partial = idx
		st.trimLocked()
	case ev.Type == loop.MessageEnd && stored.EntryID != "":
		st.retirePartialLocked()
		st.trimLocked()
	}
}

func (st *runState) retirePartialLocked() {
	if st.partial >= 0 {
		st.pending = append(st.pending, st.partial)
		st.partial = -1
	}
}

const (
	maxKeptSuperseded      = 128
	maxKeptSupersededBytes = 4 << 20
)

func (st *runState) trimLocked() {
	limit := st.minReaderPos()
	kept, bytes := 0, 0
	for i := len(st.pending) - 1; i >= 0; i-- {
		idx := st.pending[i]
		if idx == st.partial {
			continue
		}
		size := eventPayloadBytes(st.evs[idx])
		if idx < limit || kept >= maxKeptSuperseded || (kept > 0 && bytes+size > maxKeptSupersededBytes) {
			st.evs[idx] = blankEvent
			continue
		}
		kept++
		bytes += size
	}
	out := st.pending[:0]
	for _, idx := range st.pending {
		if st.evs[idx] == blankEvent {
			continue
		}
		out = append(out, idx)
	}
	st.pending = out
}

func eventPayloadBytes(ev *loop.Event) int {
	if ev == nil {
		return 0
	}
	if ev.BufferedBytes > 0 {
		return ev.BufferedBytes
	}
	n := len(ev.System) + len(ev.MessageText)
	if ev.Message != nil {
		for _, c := range ev.Message.Content {
			n += len(c.Text) + len(c.Thinking) + len(c.Data) + len(c.Input) + len(c.ArgumentsRaw)
			n += len(c.ThinkingSignature) + len(c.ThinkingData) + len(c.TextSignature) + retainedValueBytes(c.Arguments)
		}
		n += retainedValueBytes(ev.Message.Details)
	}
	if ev.AssistantMessageEvent != nil {
		n += len(ev.AssistantMessageEvent.Delta)
	}
	return n + retainedValueBytes(ev.PartialResult) + retainedValueBytes(ev.Args)
}

func retainedValueBytes(v any) int {
	switch v := v.(type) {
	case nil:
		return 0
	case string:
		return len(v)
	case []byte:
		return len(v)
	case map[string]any:
		n := 0
		for key, value := range v {
			n += len(key) + retainedValueBytes(value)
		}
		return n
	case []any:
		n := 0
		for _, value := range v {
			n += retainedValueBytes(value)
		}
		return n
	default:
		raw, _ := json.Marshal(v)
		return len(raw)
	}
}

func promptDigest(system string, tools []toolapi.Spec) string {
	h := sha256.New()
	_, _ = h.Write([]byte(system))
	b, err := json.Marshal(tools)
	if err != nil {
		b = nil
	}
	_, _ = h.Write(b)
	return string(h.Sum(nil))
}

type File struct {
	Addr      string    `json:"addr"`
	Token     string    `json:"token"`
	PID       int       `json:"pid,omitempty"`
	Build     string    `json:"build,omitempty"`
	Exe       string    `json:"exe,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
}

func New(opt Options) (*Server, error) {
	if err := config.ValidateMCPServers(opt.Config.MCPServers); err != nil {
		return nil, err
	}
	serverID, err := idgen.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate server identity: %w", err)
	}
	shells := process.DiscoverShellRuntime()
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
	providerExtensions := extension.NewProviderManager(opt.Config.Home)
	providerDiscovery := extension.Discover(opt.Config.Home, toggles.Load(opt.Config.Home).Extensions)
	if err := providerExtensions.Replace(providerDiscovery.Enabled); err != nil {
		providerExtensions.Close()
		return nil, fmt.Errorf("load provider extensions: %w", err)
	}
	if err := reg.ReplaceExtensionProviders(providerExtensions.Specs()); err != nil {
		providerExtensions.Close()
		return nil, fmt.Errorf("register provider extensions: %w", err)
	}
	requireCredential := opt.Streamer == nil
	st := opt.Streamer
	if st == nil {
		st = liveFromRegistry(reg, providerExtensions, time.Duration(opt.Config.Streaming.IdleTimeoutSeconds)*time.Second)
	}
	ws, err := workspace.Open(opt.Config.Home, opt.Config.Sessions.Root)
	if err != nil {
		return nil, fmt.Errorf("load workspace registry: %w", err)
	}
	infos, _ := session.List(opt.Config.Sessions.Root)
	cwds := make([]string, 0, len(infos))
	for _, info := range infos {
		cwds = append(cwds, info.CWD)
	}
	_ = ws.Bootstrap(cwds)
	sidx := session.NewIndex(infos)
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	outputStore, err := output.New()
	if err != nil {
		runtimeCancel()
		providerExtensions.Close()
		return nil, fmt.Errorf("create tool output store: %w", err)
	}
	srv := &Server{
		cfg:                    opt.Config,
		token:                  tok,
		serverID:               serverID,
		pid:                    os.Getpid(),
		exe:                    executablePath(),
		startedAt:              time.Now(),
		stopReq:                make(chan struct{}),
		browserSessionCookie:   "ki_session_" + serverID,
		browserCSRFCookie:      "ki_csrf_" + serverID,
		streamer:               st,
		registry:               reg,
		providerExtensions:     providerExtensions,
		requireModelCredential: requireCredential,
		providerAuth:           map[string]*providerAuthState{},
		extInstall:             newExtensionInstaller(),
		resources:              resources.NewLoader(opt.Config.Home),
		runs:                   map[string]*runState{},
		processes:              map[string]*process.Manager{},
		outputStore:            outputStore,
		agentTasks:             agent.NewController(),
		ws:                     ws,
		sidx:                   sidx,
		slist:                  session.NewListCache(),
		shells:                 shells,
		mutations:              filetools.NewMutationQueue(),
		gsubs:                  map[*pushSub]struct{}{},
		pendingReload:          map[string]bool{},
		extUI:                  map[string]map[string]*extUIState{},
		globalExtUI:            map[string]*extUIState{},
		idempotency:            map[string]extension.EnqueueResult{},
		inputGates:             map[string]*sync.Mutex{},
		activeTools:            map[string][]string{},
		uiAnswers:              map[string]chan uiAnswer{},
		browserSessions:        map[string]time.Time{},
		pushAborted:            map[string]struct{}{},
		runtime:                map[string]*runtimePrep{},
		sessionWG:              map[string]*sync.WaitGroup{},
		deleting:               map[string]bool{},
		stopping:               map[string]int{},
		deletingWorkspaces:     map[string]bool{},
		runtimeCtx:             runtimeCtx,
		runtimeCancel:          runtimeCancel,
	}
	codeConfig := codemode.Config{}
	if opt.CodeModeConfig != nil {
		codeConfig = *opt.CodeModeConfig
	}
	srv.codeMode = codemode.NewManager(codeConfig)
	srv.replay = newReplayCache()
	srv.ext = extension.NewManager(opt.Config.Home, srv.onExtensionError)
	srv.ext.SetHost(srv)
	srv.ext.SetStatusHandler(func(extension.RuntimeStatus) { srv.publishInvalidation(scopeExtensions) })
	srv.providerExtensions.SetRuntimeManager(srv.ext)
	srv.providerExtensions.SetErrorHandler(srv.onExtensionError)
	srv.providerExtensions.SetProviderAuthHandler(srv.onProviderAuthEvent)
	srv.agentTasks.SetMaxConcurrent(opt.Config.Agents.MaxConcurrent)
	srv.agentTasks.SetListener(func(snapshot agent.Snapshot) {
		event := loop.Event{Type: loop.AgentUpdated, Agent: agent.ViewSnapshot(snapshot)}
		seen := map[string]bool{}
		for _, id := range []string{snapshot.SessionID, snapshot.ParentSessionID, snapshot.RootSessionID} {
			if id != "" && !seen[id] {
				seen[id] = true
				srv.publishRuntimeUpdate(id, event)
			}
		}
	})
	srv.slist.SetChangeHandler(func() { srv.publishInvalidation(scopeSessions) })
	srv.restoreAgentTasks(infos)

	if opt.Config.Push.Enabled {
		key, err := push.LoadOrCreateKey(filepath.Join(opt.Config.Home, "vapid.json"), opt.Config.Push.Subject)
		if err != nil {
			slog.Warn("web push disabled", "err", err)
		} else {
			srv.push = push.NewService(key, push.OpenStore(filepath.Join(opt.Config.Home, "push-subscriptions.json")))
			srv.push.Start(runtimeCtx)
		}
	}
	return srv, nil
}

func liveFromRegistry(reg *provider.Registry, extensions *extension.ProviderManager, idleTimeout time.Duration) loop.Streamer {
	return &router{registry: reg, extensions: extensions, idleTimeout: idleTimeout}
}

type router struct {
	idleTimeout time.Duration
	registry    *provider.Registry
	extensions  *extension.ProviderManager
}

func (r *router) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	if r.extensions != nil && r.extensions.HasProvider(req.Provider) {
		_, model, _, err := r.registry.Resolve(req.Provider, req.Model)
		if err != nil {
			return types.Message{}, fmt.Errorf("resolve provider model: %w", err)
		}
		credential, status, err := r.registry.Credential(req.Provider)
		if err != nil {
			return types.Message{}, fmt.Errorf("resolve provider credential: %w", err)
		}
		if !status.Configured {
			return types.Message{}, fmt.Errorf("%w: %q", errProviderNoCredential, req.Provider)
		}
		credential, err = r.extensions.RefreshCredential(ctx, r.registry, req.Provider, credential)
		if err != nil {
			return types.Message{}, fmt.Errorf("refresh provider credential: %w", err)
		}
		msg, err := r.extensions.NewStreamer(model, credential).Stream(ctx, req, emit)
		if err != nil {
			return msg, fmt.Errorf("stream provider extension: %w", err)
		}
		return msg, nil
	}
	_, m, key, err := r.registry.Resolve(req.Provider, req.Model)
	if err != nil {
		return types.Message{}, fmt.Errorf("resolve provider model: %w", err)
	}
	msg, err := provider.NewLiveModel(m, key, nil).WithIdleTimeout(r.idleTimeout).Stream(ctx, req, emit)
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

func (s *Server) Token() string { return s.token }

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
	if s.ext != nil {
		s.ext.CloseExcept(active)
	}
	s.startExtensions()
	s.reloadProviderExtensions()
	s.resetRuntimeExcept(active)
	s.publishInvalidation(scopeExtensions)
	s.publishInvalidation(scopeSessions)
	return len(active) > 0
}

func (s *Server) startExtensions() {
	discovery := extension.Discover(s.cfg.Home, toggles.Load(s.cfg.Home).Extensions)
	if err := s.disableManifestExtensions(discovery.All); err != nil {
		slog.Warn("disable invalid extensions", "err", err)
		discovery = extension.Discover(s.cfg.Home, toggles.Load(s.cfg.Home).Extensions)
	}
	if s.ext != nil {
		s.ext.Start(s.runtimeCtx, discovery.Enabled)
	}
	if s.providerExtensions != nil {
		s.providerExtensions.Start(s.runtimeCtx)
	}
}

func (s *Server) reloadProviderExtensions() {
	if s.providerExtensions == nil {
		return
	}
	discovery := extension.Discover(s.cfg.Home, toggles.Load(s.cfg.Home).Extensions)
	if err := s.providerExtensions.Replace(discovery.Enabled); err != nil {
		slog.Warn("reload provider extensions", "err", err)
		return
	}
	if err := s.registry.ReplaceExtensionProviders(s.providerExtensions.Specs()); err != nil {
		slog.Warn("reload provider catalog", "err", err)
	}
	s.publishInvalidation(scopeProviders)
	s.publishInvalidation(scopeExtensions)
}

func (s *Server) reloadSession(id string) {
	s.resources.Invalidate(id)
	if s.ext != nil {
		s.ext.CloseSession(id)
	}
	s.resetRuntime(id)
	dir, err := s.sessionDir(id)
	if err != nil {
		return
	}
	header, err := session.ReadHeader(dir)
	if err != nil {
		return
	}
	s.kickWarmup(id, header.CWD)
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

func compactionEndEvent(intent compact.Intent, outcome compactionOutcome, err error) loop.Event {
	status, ok := "committed", err == nil
	switch {
	case errors.Is(err, compact.ErrNothingToCompact):
		status, ok = "empty", true
	case errors.Is(err, compact.ErrCompactionSkipped):
		status = "cancelled"
	case err != nil:
		status = "failed"
	}
	return loop.Event{
		Type: loop.CompactionEnd, Reason: string(intent.Reason), OK: ok,
		WillRetry: intent.WillRetry, Status: status,
		EntryID: outcome.Entry.ID, Strategy: outcome.Strategy,
		FromExtension:    outcome.FromExtension,
		FirstKeptEntryID: outcome.FirstKeptEntryID,
		TokensBefore:     outcome.TokensBefore, Usage: outcome.Usage,
	}
}

func (s *Server) publishStandaloneCompactionEvent(ctx context.Context, sess *session.Session, ev loop.Event) error {
	if err := persistCompactionEvent(sess, &ev); err != nil {
		return fmt.Errorf("append manual compaction event: %w", err)
	}
	s.publishPush(sess.ID(), ev)
	if s.ext != nil && !isQuestionSide(sess.Config) {
		s.ext.OnEvent(ctx, sess.ID(), extension.RedactEvent(ev, sess.ID()))
	}
	return nil
}

func (s *Server) publishContextUsage(sess *session.Session) {
	_, info, ok := s.registry.FindModel(sess.Config.Provider, sess.Config.Model)
	if !ok {
		return
	}
	used, window, err := s.contextUsageEstimate(sess, info, s.providerBinding(info), nil)
	if err != nil {
		slog.Warn("marshal tool schemas", "session_id", sess.ID(), "err", err)
	}
	if _, err := sess.AppendContextUsage(used, window, true); err != nil {
		slog.Warn("append context usage", "session_id", sess.ID(), "err", err)
		return
	}
	s.publishPush(sess.ID(), loop.Event{
		Type:           loop.ContextUsage,
		Provider:       sess.Config.Provider,
		Model:          sess.Config.Model,
		CatalogVersion: provider.CatalogVersion,
		UsedTokens:     used,
		ContextWindow:  window,
		Estimated:      true,
	})
}

func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": buildinfo.Version})
	})
	api.HandleFunc("GET /v1/auth/status", s.authStatus)
	api.HandleFunc("POST /v1/auth/login", s.login)
	api.HandleFunc("POST /v1/auth/logout", s.auth(s.logout))
	api.HandleFunc("GET /v1/models", s.auth(s.models))
	api.HandleFunc("GET /v1/providers", s.auth(s.providers))
	api.HandleFunc("POST /v1/providers", s.auth(s.createProvider))
	api.HandleFunc("PATCH /v1/providers/{id}", s.auth(s.patchProvider))
	api.HandleFunc("DELETE /v1/providers/{id}", s.auth(s.deleteProvider))
	api.HandleFunc("PUT /v1/providers/{id}/credential", s.auth(s.putProviderCredential))
	api.HandleFunc("POST /v1/providers/{id}/auth/login", s.auth(s.startProviderAuth))
	api.HandleFunc("GET /v1/providers/{id}/auth/{requestId}", s.auth(s.providerAuthStatus))
	api.HandleFunc("POST /v1/providers/{id}/auth/{requestId}/input", s.auth(s.providerAuthInput))
	api.HandleFunc("POST /v1/providers/{id}/auth/{requestId}/cancel", s.auth(s.cancelProviderAuth))
	api.HandleFunc("POST /v1/providers/{id}/auth/logout", s.auth(s.logoutProviderAuth))
	api.HandleFunc("POST /v1/providers/{id}/models", s.auth(s.createProviderModel))
	api.HandleFunc("POST /v1/providers/{id}/models/refresh", s.auth(s.refreshProviderModels))
	api.HandleFunc("PATCH /v1/providers/{id}/models", s.auth(s.patchProviderModel))
	api.HandleFunc("DELETE /v1/providers/{id}/models", s.auth(s.deleteProviderModel))
	api.HandleFunc("PUT /v1/default-model", s.auth(s.putDefaultModel))
	api.HandleFunc("GET /v1/meta", s.auth(s.meta))
	api.HandleFunc("POST /v1/shutdown", s.auth(s.shutdown))
	api.HandleFunc("GET /v1/events", s.auth(s.pushEvents))
	api.HandleFunc("GET /v1/push/config", s.auth(s.pushConfig))
	api.HandleFunc("POST /v1/push/subscriptions", s.auth(s.putPushSubscription))
	api.HandleFunc("DELETE /v1/push/subscriptions", s.auth(s.deletePushSubscription))
	api.HandleFunc("GET /v1/sessions", s.auth(s.list))
	api.HandleFunc("POST /v1/sessions", s.auth(s.create))
	api.HandleFunc("GET /v1/sessions/search", s.auth(s.searchSessions))
	api.HandleFunc("GET /v1/sessions/{id}", s.auth(s.get))
	api.HandleFunc("PATCH /v1/sessions/{id}", s.auth(s.patch))
	api.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.deleteSession))
	api.HandleFunc("POST /v1/sessions/{id}/prompt", s.auth(s.prompt))
	api.HandleFunc("POST /v1/sessions/{id}/question-action", s.auth(s.questionAction))
	api.HandleFunc("GET /v1/sessions/{id}/events", s.auth(s.events))
	api.HandleFunc("POST /v1/sessions/{id}/abort", s.auth(s.abort))
	api.HandleFunc("POST /v1/sessions/{id}/extension-ui", s.auth(s.extensionUIAnswer))
	api.HandleFunc("POST /v1/sessions/{id}/compact", s.auth(func(w http.ResponseWriter, r *http.Request) {
		s.doCompact(w, r)
	}))
	api.HandleFunc("POST /v1/sessions/{id}/fork", s.auth(s.fork))
	api.HandleFunc("POST /v1/sessions/{id}/attachments", s.auth(s.uploadAttachment))
	api.HandleFunc("POST /v1/reload", s.auth(s.doReload))
	api.HandleFunc("GET /v1/skills", s.auth(s.getSkills))
	api.HandleFunc("PATCH /v1/skills", s.auth(s.patchSkills))
	api.HandleFunc("GET /v1/tools", s.auth(s.getTools))
	api.HandleFunc("PATCH /v1/tools", s.auth(s.patchTools))
	api.HandleFunc("GET /v1/extensions", s.auth(s.getExtensions))
	api.HandleFunc("PATCH /v1/extensions", s.auth(s.patchExtensions))
	api.HandleFunc("GET /v1/extensions/available", s.auth(s.getAvailableExtensions))
	api.HandleFunc("POST /v1/extensions/{name}/install", s.auth(s.installExtension))
	api.HandleFunc("GET /v1/commands", s.auth(s.getCommands))
	api.HandleFunc("GET /v1/prompt/append", s.auth(s.getPromptAppend))
	api.HandleFunc("PUT /v1/prompt/append", s.auth(s.putPromptAppend))
	api.HandleFunc("DELETE /v1/prompt/append", s.auth(s.deletePromptAppend))
	api.HandleFunc("GET /v1/extensions/{name}/config", s.auth(s.getExtensionConfig))
	api.HandleFunc("PATCH /v1/extensions/{name}/config", s.auth(s.patchExtensionConfig))
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
	return gzipHandler(recoverHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			if r.URL.Path != "/v1/auth/status" && r.URL.Path != "/v1/health" {
				if bound := r.Header.Get("X-Ki-Server-ID"); bound != "" && bound != s.serverID {
					w.Header().Set("Cache-Control", "no-store")
					http.Error(w, "server instance changed", http.StatusMisdirectedRequest)
					return
				}
			}
			api.ServeHTTP(w, r)
			return
		}
		s.serveUI(w, r)
	})))
}

func recoverHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if logging.Recover("http handler panic", "method", r.Method, "path", r.URL.Path) {
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source := s.authenticate(r)
		if source == authNone {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if source == authBrowserSession {
			s.renewBrowserSession(w, r)
			if unsafeMethod(r.Method) && !s.validCSRF(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) authenticate(r *http.Request) authSource {
	if got := bearerToken(r); got != "" && sameSecret(got, s.token) {
		return authBearer
	}
	if cookie, err := r.Cookie(s.browserSessionCookie); err == nil && s.validBrowserSession(cookie.Value) {
		return authBrowserSession
	}
	return authNone
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}

func sameSecret(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func unsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (s *Server) validBrowserSession(value string) bool {
	if value == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, expiresAt := range s.browserSessions {
		if !expiresAt.After(now) {
			delete(s.browserSessions, id)
		}
	}
	expiresAt, ok := s.browserSessions[value]
	return ok && expiresAt.After(now)
}

func (s *Server) renewBrowserSession(w http.ResponseWriter, r *http.Request) {
	sessionCookie, err := r.Cookie(s.browserSessionCookie)
	if err != nil {
		return
	}
	csrfCookie, err := r.Cookie(s.browserCSRFCookie)
	if err != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	expiresAt, ok := s.browserSessions[sessionCookie.Value]
	if !ok || expiresAt.After(now.Add(browserSessionTTL/2)) {
		s.mu.Unlock()
		return
	}
	s.browserSessions[sessionCookie.Value] = now.Add(browserSessionTTL)
	s.mu.Unlock()
	s.setBrowserCookies(w, sessionCookie.Value, csrfCookie.Value, now, requestIsSecure(r))
}

func (s *Server) setBrowserCookies(w http.ResponseWriter, sessionID, csrf string, now time.Time, secure bool) {
	expires := now.Add(browserSessionTTL)
	maxAge := int(browserSessionTTL / time.Second)
	http.SetCookie(w, &http.Cookie{
		Name:     s.browserSessionCookie,
		Value:    sessionID,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     s.browserCSRFCookie,
		Value:    csrf,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(s.browserCSRFCookie)
	if err != nil {
		return false
	}
	return sameSecret(r.Header.Get("X-Ki-CSRF"), cookie.Value)
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":  s.authenticate(r) != authNone,
		"serverId":       s.serverID,
		"csrfCookieName": s.browserCSRFCookie,
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !sameSecret(strings.TrimSpace(body.Token), s.token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := newToken()
	csrf := newToken()
	now := time.Now()
	s.mu.Lock()
	for id, expiresAt := range s.browserSessions {
		if !expiresAt.After(now) {
			delete(s.browserSessions, id)
		}
	}
	s.browserSessions[sessionID] = now.Add(browserSessionTTL)
	s.mu.Unlock()

	s.setBrowserCookies(w, sessionID, csrf, now, requestIsSecure(r))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.browserSessionCookie); err == nil {
		s.mu.Lock()
		delete(s.browserSessions, cookie.Value)
		s.mu.Unlock()
	}
	secure := requestIsSecure(r)
	for _, name := range []string{s.browserSessionCookie, s.browserCSRFCookie} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == s.browserSessionCookie,
			Secure:   secure,
			SameSite: http.SameSiteStrictMode,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(forwarded, "https")
}

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
	handshake := File{
		Addr:      dialAddress(ln.Addr()),
		Token:     s.token,
		PID:       s.pid,
		Build:     buildinfo.Version,
		Exe:       s.exe,
		StartedAt: s.startedAt,
	}
	if err := WriteServerFile(s.cfg.Home, handshake); err != nil {
		slog.Warn("server.json", "err", err)
	}
	slog.Info("listen", "addr", ln.Addr().String())
	s.startExtensions()
	err = httpSrv.Serve(ln)
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if rmErr := RemoveServerFile(s.cfg.Home, s.token); rmErr != nil {
		slog.Warn("server.json", "err", rmErr)
	}
	return nil
}

func dialAddress(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	if host == "" || net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func (s *Server) Addr() string {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return ""
	}
	return ln.Addr().String()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.runtimeMu.Lock()
	if s.shutdownDone == nil {
		s.shutdownDone = make(chan struct{})
		s.runtimeClosed = true
		if s.runtimeCancel != nil {
			s.runtimeCancel()
		}
		go s.shutdownOwned()
	}
	done := s.shutdownDone
	s.runtimeMu.Unlock()
	select {
	case <-done:
		return s.shutdownErr
	default:
	}
	select {
	case <-done:
		return s.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) shutdownOwned() {
	defer close(s.shutdownDone)
	if s.slist != nil {
		s.slist.Close()
	}
	s.mu.Lock()
	for id, st := range s.runs {
		s.cancelRun(id, st, cancelReasonServerShutdown, "server", false)
	}
	jobs := make([]*process.Manager, 0, len(s.processes))
	for _, manager := range s.processes {
		jobs = append(jobs, manager)
	}
	s.mu.Unlock()
	managersDone := make(chan struct{})
	go func() {
		defer close(managersDone)
		var owners sync.WaitGroup
		for _, manager := range jobs {
			owners.Go(manager.Close)
		}
		owners.Wait()
	}()
	agentsDone := make(chan struct{})
	go func() {
		defer close(agentsDone)
		if s.agentTasks != nil {
			_ = s.agentTasks.CloseContext(context.Background())
		}
	}()
	s.creationMu.Lock()
	s.creationMu.Unlock()
	s.runtimeWG.Wait()
	<-agentsDone
	<-managersDone
	if s.codeMode != nil {
		_ = s.codeMode.Close()
	}
	s.shutdownErr = errors.Join(s.shutdownErr, s.closeMCPManagers())
	s.mu.Lock()
	s.replay.closed = true
	if s.replay.timer != nil {
		s.replay.timer.Stop()
	}
	for id := range s.replay.items {
		if st := s.runs[id]; st == s.replay.items[id].Value.(completedReplay).state {
			delete(s.runs, id)
		}
		s.forgetReplayLocked(id)
	}
	for id := range s.processes {
		delete(s.processes, id)
	}
	s.mu.Unlock()
	if s.outputStore != nil {
		_ = s.outputStore.Close()
	}
	if s.push != nil {
		s.push.Close()
	}
	if s.ext != nil {
		s.ext.Close()
	}
	if s.providerExtensions != nil {
		s.providerExtensions.Close()
	}
	s.mu.Lock()
	httpSrv := s.http
	s.mu.Unlock()
	if httpSrv == nil {
		return
	}
	s.shutdownErr = errors.Join(s.shutdownErr, httpSrv.Shutdown(context.Background()))
}

func (s *Server) processesFor(id string) *process.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeMu.Lock()
	closed := s.runtimeClosed || s.deleting[id] || s.stopping[id] > 0
	s.runtimeMu.Unlock()
	if closed {
		if s.closedProcesses == nil {
			s.closedProcesses = process.NewManager()
			s.closedProcesses.Close()
		}
		return s.closedProcesses
	}
	if jobs, ok := s.processes[id]; ok {
		return jobs
	}
	jobs := process.NewSpooledManager(s.outputStore, id)
	jobs.SetListener(func(update process.Update) {
		s.publishRuntimeUpdate(id, loop.Event{Type: loop.ProcessUpdated, Process: update.Process})
	})
	s.processes[id] = jobs
	return jobs
}

func (s *Server) closeProcesses(id string) {
	s.mu.Lock()
	jobs := s.processes[id]
	delete(s.processes, id)
	s.mu.Unlock()
	if jobs != nil {
		jobs.Close()
	}
	if s.codeMode != nil {
		_ = s.codeMode.CloseSession(id)
	}
	if s.outputStore != nil {
		_ = s.outputStore.CloseSession(id)
	}
}

func WriteServerFile(home string, f File) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, "server.json.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(home, "server.json"))
}

func RemoveServerFile(home, token string) error {
	current, err := ReadServerFile(home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if token != "" && current.Token != token {
		return nil
	}
	if err := os.Remove(filepath.Join(home, "server.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

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
	s.creationMu.Lock()
	defer s.creationMu.Unlock()
	if err := s.sessionAdmissionError(""); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	var body struct {
		CWD            string         `json:"cwd"`
		WorkspaceID    string         `json:"workspaceId"`
		Model          string         `json:"model"`
		ThinkingEffort string         `json:"thinkingEffort"`
		Metadata       map[string]any `json:"metadata"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for key := range body.Metadata {
		switch key {
		case "purpose", "sourceSessionId", "sourceQuestionItemId", "questionItemId", "sideBoundaryEntryId",
			"questionSideResources", "readOnlyWorkspaceRoot", "readOnlyAttachmentsRoot", "readOnlyProfile", "questionSideInitializing", "sideInitialization":
			http.Error(w, "reserved session metadata: "+key, http.StatusBadRequest)
			return
		}
	}
	rec, err := s.resolveWorkspace(body.WorkspaceID, body.CWD, "")
	if err != nil {
		code := 400
		if workspace.NotFound(err) {
			code = 404
		}
		http.Error(w, err.Error(), code)
		return
	}
	if s.deletingWorkspaces[rec.ID] {
		http.Error(w, errSessionNotFound.Error(), http.StatusConflict)
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
	sess, err := session.CreateWithOptions(root, rec.Path, ref.Provider, ref.Model, session.CreateOptions{
		ThinkingEffort: effort,
		Metadata:       body.Metadata,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = sess.Close() }()
	_ = s.ws.AttachSession(rec.ID, sess.ID())
	s.sidx.Add(sess.ID(), sess.Dir)
	s.rememberModel(ref)
	writeJSON(w, 200, s.sessionMap(sess, nil))
	//nolint:contextcheck // session warmup is process-owned via runtimeCtx, not the HTTP request
	s.kickWarmup(sess.ID(), sess.Header.CWD)
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
}

func (s *Server) open(id string) (*session.Session, error) {
	dir, err := s.sessionDir(id)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	header, err := session.ReadHeader(dir)
	if err != nil {
		return nil, fmt.Errorf("read session header: %w", err)
	}
	cfg, err := session.ReadConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("read session config: %w", err)
	}
	entries, err := session.AllEntries(dir)
	if err != nil {
		return nil, fmt.Errorf("read session entries: %w", err)
	}
	return session.OpenFrom(dir, header, cfg, entries), nil
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	fields := q.Get("fields")
	entryID := q.Get("entry")
	batch := q.Get("entries")
	before := q.Get("before")
	view := q.Get("view")
	if view != "" && view != "detailed" && view != "compact" && view != "trace" && view != "inspect" {
		http.Error(w, "invalid session view", http.StatusBadRequest)
		return
	}
	compact := view == "compact"
	traceView := view == "trace"
	inspectView := view == "inspect"
	turnID := q.Get("turn")
	keep := 1
	if raw := q.Get("keep"); raw != "" {
		var err error
		keep, err = strconv.Atoi(raw)
		if err != nil || keep < 0 || keep > 20 {
			http.Error(w, "invalid compact keep", http.StatusBadRequest)
			return
		}
	}
	limit := session.DefaultViewLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = session.ClampViewLimit(n)
	}
	withIndex := hasField(fields, "index")
	withSystem := hasField(fields, "system")
	projectionOnly := (hasField(fields, "runtime") || withSystem) && !withIndex && entryID == "" && batch == "" && before == "" && turnID == ""
	full := entryID != "" || batch != "" || before != "" || withIndex || compact || traceView || inspectView || turnID != "" || withSystem

	var snap *sessionSnap
	var err error
	switch {
	case full && !traceView && !inspectView:
		snap, err = s.loadIndexedSessionSnap(id)
	case full:
		snap, err = s.loadSessionSnap(id, true, 0)
	case projectionOnly:
		snap, err = s.loadSessionSnap(id, false, 0)
	default:
		snap, err = s.loadIndexedSessionSnap(id)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if isQuestionSide(snap.configV) {
		s.getQuestionSide(w, r, snap, limit, keep)
		return
	}

	if traceView {
		contextLines := 0
		if raw := q.Get("context"); raw != "" {
			contextLines, err = strconv.Atoi(raw)
			if err != nil || contextLines < 0 || contextLines > 20 {
				http.Error(w, "invalid trace context", http.StatusBadRequest)
				return
			}
		}
		filter := session.TraceFilter{
			Types: splitQueryList(q.Get("type")), Roles: splitQueryList(q.Get("role")),
			Tools: splitQueryList(q.Get("tool")), FailedOnly: queryBool(q.Get("failed")),
			CacheMisses: queryBool(q.Get("cacheMiss")), Since: q.Get("since"), Until: q.Get("until"),
		}
		rows := session.TraceWithContext(snap.entries, snap.leafID, filter, contextLines)
		if before != "" {
			at := slices.IndexFunc(rows, func(row session.TraceEntry) bool { return row.ID == before })
			if at < 0 {
				http.Error(w, "trace cursor not found on active branch", http.StatusConflict)
				return
			}
			rows = rows[:at]
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[len(rows)-limit:]
		}
		oldest := ""
		if len(rows) > 0 {
			oldest = rows[0].ID
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"schemaVersion": 1, "id": id, "leafId": snap.leafID,
			"trace": rows, "hasMore": hasMore, "oldestId": oldest,
		})
		return
	}
	if inspectView {
		infos, listErr := s.slist.List(s.cfg.Sessions.Root)
		if listErr != nil {
			http.Error(w, listErr.Error(), http.StatusInternalServerError)
			return
		}
		children := make([]session.Info, 0)
		for _, info := range infos {
			if info.ParentSessionID == id {
				children = append(children, info)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"schemaVersion": 1, "id": id, "leafId": snap.leafID,
			"cwd": snap.header.CWD, "provider": snap.configV.Provider, "model": snap.configV.Model,
			"parentSessionId": snap.header.ParentSession, "children": children,
			"analysis": session.Analyze(snap.entries, snap.leafID),
		})
		return
	}

	if entryID != "" {
		got, err := snap.transcript.Lookup([]string{entryID})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if len(got) == 0 {
			http.Error(w, "entry not found", http.StatusNotFound)
			return
		}
		for i := range got {
			if got[i].Type == "question_requested" || got[i].Type == "question_updated" {
				public, decodeErr := session.PublicQuestionEntry(got[i])
				if decodeErr != nil {
					http.Error(w, decodeErr.Error(), http.StatusInternalServerError)
					return
				}
				got[i] = public
			}
		}
		got = session.WithCacheMisses(got, snap.entries, snap.leafID)
		writeJSON(w, 200, map[string]any{"entry": got[0]})
		return
	}
	if batch != "" {
		got, err := snap.transcript.Lookup(strings.Split(batch, ","))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		for i := range got {
			if got[i].Type == "question_requested" || got[i].Type == "question_updated" {
				public, decodeErr := session.PublicQuestionEntry(got[i])
				if decodeErr != nil {
					http.Error(w, decodeErr.Error(), http.StatusInternalServerError)
					return
				}
				got[i] = public
			}
		}
		got = session.WithCacheMisses(got, snap.entries, snap.leafID)
		writeJSON(w, 200, map[string]any{"entries": got})
		return
	}
	if before != "" && !slices.ContainsFunc(session.LeafChain(snap.entries, snap.leafID), func(e session.Entry) bool { return e.ID == before }) {
		http.Error(w, "history cursor not found on active branch", http.StatusConflict)
		return
	}
	if turnID != "" {
		if compact {
			page, found, err := snap.transcript.Compact(snap.leafID, "", turnID, keep)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if !found {
				http.Error(w, "turn not found on active branch", http.StatusNotFound)
				return
			}
			page.Entries = session.WithCacheMisses(page.Entries, snap.entries, snap.leafID)
			writeJSON(w, 200, page)
			return
		}
		page, found, err := snap.transcript.Tail(snap.leafID, before, turnID, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if !found {
			if before != "" {
				if _, exists := session.BuildTurn(snap.entries, snap.leafID, turnID, "", limit); exists {
					http.Error(w, "history cursor not found in requested turn", http.StatusConflict)
					return
				}
			}
			http.Error(w, "turn not found on active branch", http.StatusNotFound)
			return
		}
		page.Entries = session.WithCacheMisses(page.Entries, snap.entries, snap.leafID)
		writeJSON(w, 200, map[string]any{"entries": page.Entries, "hasMore": page.HasMore, "oldestId": page.OldestID})
		return
	}
	if before != "" {
		if compact {
			page, _, err := snap.transcript.Compact(snap.leafID, before, "", keep)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			page.Entries = session.WithCacheMisses(page.Entries, snap.entries, snap.leafID)
			writeJSON(w, 200, page)
			return
		}
		view, _, err := snap.transcript.Tail(snap.leafID, before, "", limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		view.Entries = session.WithCacheMisses(view.Entries, snap.entries, snap.leafID)
		writeJSON(w, 200, map[string]any{
			"entries":  view.Entries,
			"hasMore":  view.HasMore,
			"oldestId": view.OldestID,
		})
		return
	}
	if withIndex && !hasField(fields, "runtime") {
		body, err := json.Marshal(map[string]any{"id": id, "index": snap.index()})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		return
	}

	runtime, err := s.sessionRuntime(snap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runtime["questions"] = s.questionsProjection(id)
	if projectionOnly {
		if withSystem {
			runtime["systemPrompt"] = snap.systemPrompt()
		}
		writeJSON(w, 200, s.sessionMapSnap(snap, runtime))
		return
	}

	runtime["leafId"] = snap.leafID
	if compact {
		page, _, err := snap.transcript.Compact(snap.leafID, "", "", keep)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		page.Entries = session.WithCacheMisses(page.Entries, snap.entries, snap.leafID)
		runtime["entries"], runtime["compactTurns"] = page.Entries, page.Turns
		runtime["hasMore"], runtime["oldestId"] = page.HasMore, page.OldestID
	} else {
		tail := session.BuildTail(snap.entries, snap.leafID, limit, snap.complete)
		if snap.transcript != nil {
			tail, _, err = snap.transcript.Tail(snap.leafID, "", "", limit)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		tail.Entries = session.WithCacheMisses(tail.Entries, snap.entries, snap.leafID)
		runtime["entries"], runtime["hasMore"], runtime["oldestId"] = tail.Entries, tail.HasMore, tail.OldestID
	}
	if withIndex || (!compact && snap.complete && snap.small) {
		runtime["index"] = snap.index()
	}
	writeJSON(w, 200, s.sessionMapSnap(snap, runtime))
}

func splitQueryList(raw string) []string {
	var out []string
	for item := range strings.SplitSeq(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func queryBool(raw string) bool {
	value, _ := strconv.ParseBool(raw)
	return value
}

func hasField(fields, name string) bool {
	for f := range strings.SplitSeq(fields, ",") {
		if strings.TrimSpace(f) == name {
			return true
		}
	}
	return false
}

func (s *Server) sessionRuntime(snap *sessionSnap) (map[string]any, error) {
	snapshot := s.resources.Load(snap.id, snap.header.CWD)
	sk := s.sessionCatalog(snapshot)
	tg := toggles.Load(s.cfg.Home)
	mcpSnapshot, err := s.resolveMCP(snap.header.CWD)
	if err != nil {
		return nil, err
	}
	mcpInfo := s.mcpInfos(mcpSnapshot, tg.MCP)
	queued, err := session.ReadQueue(snap.dir)
	if err != nil {
		return nil, err
	}
	if queued == nil {
		queued = []session.QueuedItem{}
	}
	extQueued, err := session.ReadExtQueue(snap.dir)
	if err != nil {
		return nil, err
	}
	if extQueued == nil {
		extQueued = []session.ExtQueuedItem{}
	}
	//nolint:contextcheck // session warmup is process-owned via runtimeCtx, not the HTTP request
	s.kickWarmup(snap.id, snap.header.CWD)
	ready := s.runtimeReady(snap.id)
	cmds := command.Catalog(snapshot, tg.Skills)
	if s.ext != nil {
		owner := map[string]string{}
		for extName, contrib := range s.ext.SessionContributions(snap.id) {
			for _, cmd := range contrib.Commands {
				if _, exists := owner[cmd.Name]; !exists {
					owner[cmd.Name] = extName
				}
			}
		}
		for _, spec := range s.ext.RuntimeCommands(snap.id) {
			cmds = append(cmds, command.Item{
				Name: spec.Name, Description: spec.Description, ArgumentHint: spec.ArgumentHint, Completions: spec.Completions,
				Source: "extension", Extension: owner[spec.Name],
			})
		}
	}
	return map[string]any{
		"processes":           s.processSnapshots(snap.id),
		"agents":              s.agentSnapshots(snap.id),
		"leafId":              snap.leafID,
		"availableSkills":     sk,
		"availableExtensions": s.extensionCatalog(snapshot, snap.id),
		"availableMCP":        mcpInfo,
		"commands":            cmds,
		"queued":              queued,
		"extQueued":           extQueued,
		"extensionUi":         s.extensionUIList(snap.id),
		"runtime":             map[string]any{"ready": ready},
	}, nil
}

func (s *Server) sessionCatalog(snapshot resources.Snapshot) []map[string]any {
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
	slices.SortFunc(sk, func(a, b map[string]any) int { return cmp.Compare(mapName(a), mapName(b)) })
	return sk
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
			"execToolType":       m.ExecToolType,
			"compaction":         m.Compaction,
			"reasoning":          m.Reasoning,
			"fastServiceTier":    m.FastServiceTier,
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
	out["version"] = buildinfo.Version
	out["releaseTag"] = buildinfo.ReleaseTag
	out["pid"] = s.pid
	out["startedAt"] = s.startedAt
	out["exe"] = s.exe
	out["kiHome"] = s.cfg.Home
	writeJSON(w, 200, out)
}

func (s *Server) StopRequested() <-chan struct{} { return s.stopReq }

func (s *Server) RequestStop() {
	s.stopOnce.Do(func() { close(s.stopReq) })
}

func (s *Server) shutdown(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "pid": s.pid})
	slog.Info("shutdown requested", "pid", s.pid)
	s.RequestStop()
}

func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request) {
	gate := s.inputGate(r.PathValue("id"))
	gate.Lock()
	defer gate.Unlock()
	if err := s.sessionAdmissionError(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
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
	modelSettingsRequested := body.Model != "" || body.ThinkingEffort != nil
	if body.LeafID != nil && !questionSideTargetAllowed(sess, *body.LeafID) {
		http.Error(w, "cannot select history before the side conversation boundary", http.StatusConflict)
		return
	}
	selectionChanged := body.Model != "" || body.ThinkingEffort != nil || body.LeafID != nil
	var modelRef provider.ModelRef
	var modelEffort string
	if modelSettingsRequested {
		ref, model, err := s.resolveSessionModel(sess, body.Model)
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
		modelRef = ref
		modelEffort = effort
	}
	if selectionChanged {
		status := http.StatusInternalServerError
		err := s.mutateIdleSession(sess.ID(), func() error {
			if modelSettingsRequested {
				status = http.StatusInternalServerError
				if err := sess.SetModelAndThinking(modelRef.Provider, modelRef.Model, modelEffort); err != nil {
					return err
				}
			}
			if body.LeafID != nil {
				status = http.StatusBadRequest
				return sess.SetLeaf(*body.LeafID)
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, errSessionBusy) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		if modelSettingsRequested {
			s.rememberModel(modelRef)
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
	if modelSettingsRequested {
		if err := s.syncQuestionSideModels(sess); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.publishContextUsage(sess)
	}
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
	writeJSON(w, 200, s.sessionMap(sess, nil))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	infos, err := s.slist.List(s.cfg.Sessions.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	activity := s.sessionActivity(infos)
	workspaceIDs := s.sessionWorkspaceIDs(infos)
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		if metadataString(info.Metadata, "purpose") == questionSidePurpose &&
			metadataString(info.Metadata, "sideBoundaryEntryId") == "" {
			continue
		}
		out = append(out, s.infoMap(info, activity, workspaceIDs[info.CWD]))
	}
	body, err := json.Marshal(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(body, '\n'))
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

func (s *Server) mutateIdleSession(id string, mutate func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.runs[id]; st != nil {
		select {
		case <-st.done:
		default:
			return errSessionBusy
		}
	}
	return mutate()
}

func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Text            string                `json:"text"`
		Content         []types.Content       `json:"content"`
		ParentID        *string               `json:"parentId"`
		Model           string                `json:"model"`
		Delivery        string                `json:"delivery"`
		QueueID         string                `json:"queueId"`
		ClientRequestID string                `json:"clientRequestId"`
		QuestionReply   *questionReplyRequest `json:"questionReply"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.QuestionReply != nil {
		if body.Text != "" || len(body.Content) != 0 || body.ParentID != nil || body.QueueID != "" || body.Model != "" || body.Delivery != "" {
			http.Error(w, "questionReply cannot be combined with text, content, parentId, queueId, model or delivery", http.StatusBadRequest)
			return
		}
		s.submitQuestionReply(w, r, *body.QuestionReply)
		return
	}
	if body.ClientRequestID == "" {
		id, err := idgen.NewV7()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		body.ClientRequestID = id
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
	sideSession := isQuestionSide(sess.Config)
	if sideSession && parsed.Kind != command.KindPrompt {
		http.Error(w, "commands are not available in a read-only side conversation", http.StatusBadRequest)
		return
	}
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	if isQuestionSide(sess.Config) && body.ParentID != nil && !questionSideTargetAllowed(sess, *body.ParentID) {
		http.Error(w, "side conversation parent must be within side history", http.StatusBadRequest)
		return
	}
	runtimeCmds := map[string]struct{}{}
	parsed = command.ResolveCommand(parsed, snapshot, runtimeCmds)
	if parsed.Kind == command.KindUnknown && s.ext != nil {
		tgPre := toggles.Load(s.cfg.Home)
		s.ext.Prepare(r.Context(), id, sess.Header.CWD, extension.Enabled(snapshot.Extensions, tgPre.Extensions))
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
		if parsed.Kind == command.KindBuiltin && ((parsed.Name == "reload" || parsed.Name == "new") && parsed.Args != "") {
			_ = sess.Close()
			writeHandled(w, "usage: /"+parsed.Name, true)
			return
		}
		if parsed.Kind == command.KindBuiltin && parsed.Name == "cd" && strings.TrimSpace(parsed.Args) == "" {
			_ = sess.Close()
			writeHandled(w, "usage: /cd <path>", true)
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
			//nolint:contextcheck // /new warmup is process-owned; must not cancel with this prompt request
			s.handleBuiltin(w, r, parsed.Name, parsed.Args)
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
		if hasUnresolvedToolCalls(messages) && !sess.IsInterruptedToolTail(*body.ParentID) {
			_ = sess.Close()
			http.Error(w, "parent leaves unresolved tool calls", http.StatusBadRequest)
			return
		}
	}
	if queueID != "" {
		s.promoteQueued(w, r, id, sess, live, queueID, body.Model)
		return
	}
	ref, selectedModel, err := s.resolveSessionModel(sess, body.Model)
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
		gate := s.inputGate(id)
		gate.Lock()
		defer gate.Unlock()
		if err := s.sessionAdmissionError(id); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if delivery == toggles.BusySteer && s.pushSteerRun(live, steerRequest{Content: body.Content, ClientRequestID: body.ClientRequestID}) {
			writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "steered", "clientRequestId": body.ClientRequestID})
			return
		}
		if _, err := session.EnqueueMessage(dir, types.Message{Content: body.Content, ClientRequestID: body.ClientRequestID}, session.QueueHumanLane); err != nil {
			if errors.Is(err, session.ErrQueueFull) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.publishQueueChanged(id)
		writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "queued", "clientRequestId": body.ClientRequestID})
		return
	}
	_ = sess.Close()
	if s.ext != nil && !sideSession {
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
	s.startRun(context.WithoutCancel(r.Context()), w, id, body.Content, body.ParentID, body.Model, body.ClientRequestID)
}

func (s *Server) promoteQueued(w http.ResponseWriter, r *http.Request, id string, sess *session.Session, live *runState, queueID, model string) {
	gate := s.inputGate(id)
	gate.Lock()
	defer gate.Unlock()
	if err := s.sessionAdmissionError(id); err != nil {
		_ = sess.Close()
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
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
	ref, selectedModel, err := s.resolveSessionModel(sess, model)
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
	if s.pushSteerRun(live, steerRequest{Content: item.Content, Origin: item.Origin, ClientRequestID: item.ClientRequestID, Completion: item.Completion}) {
		s.publishQueueChanged(id)
		writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "steered", "clientRequestId": item.ClientRequestID})
		return
	}
	st, ctx, err := s.occupy(context.WithoutCancel(r.Context()), id)
	if err != nil {
		putBack()
		writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "queued", "clientRequestId": item.ClientRequestID})
		return
	}
	enableRunInbox(st)
	st.inputMetadata = types.Message{ClientRequestID: item.ClientRequestID, Completion: item.Completion}
	go s.runPrompt(ctx, st, id, item.Content, nil, model, item.Origin, "", s.takeNextTurn(id))
	s.publishQueueChanged(id)
	writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "started", "clientRequestId": item.ClientRequestID})
}

func (s *Server) handleBuiltin(w http.ResponseWriter, r *http.Request, name, args string) {
	switch name {
	case "new":
		result, err := s.NewSession(r.PathValue("id"), "")
		if err != nil {
			writeHandled(w, err.Error(), true)
			return
		}
		writeJSON(w, 200, map[string]any{"handled": true, "notice": "started a new session", "sessionId": result.SessionID, "cwd": result.CWD, "workspaceId": result.WorkspaceID})
	case "cd":
		result, err := s.NewSession(r.PathValue("id"), strings.TrimSpace(args))
		if err != nil {
			writeHandled(w, err.Error(), true)
			return
		}
		writeJSON(w, 200, map[string]any{"handled": true, "notice": "working directory changed to " + result.CWD, "sessionId": result.SessionID, "cwd": result.CWD, "workspaceId": result.WorkspaceID})
	case "reload":
		queued := s.requestReload(r.PathValue("id"))
		if queued {
			writeHandled(w, "reload queued until the current run finishes", false)
		} else {
			writeHandled(w, "reloaded session resources and extensions", false)
		}
	case "compact":
		s.doCompact(w, r, strings.TrimSpace(args))
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
				st, err := os.Stat(abs)
				if err != nil || !st.Mode().IsRegular() {
					return fmt.Errorf("content[%d]: %w", i, errAttachmentUnreadable)
				}
				c.Path = abs
				c.Size = st.Size()
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
				label := cmp.Or(c.Name, filepath.Base(c.Path))
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

func (s *Server) runPrompt(ctx context.Context, st *runState, id string, content []types.Content, parentID *string, reqModel, origin, idempotencyKey string, nextTurn []session.ExtQueuedItem, external ...map[string]string) {
	//nolint:contextcheck // release may rewarm/reload/dispatch after the run ctx is cancelled
	defer func() {
		logging.Recover("prompt panic", "session_id", id)
		s.preserveRunInbox(id, st)
		s.release(id, st)
	}()
	sess, err := s.open(id)
	if err != nil {
		st.err = err
		return
	}
	defer func() { _ = sess.Close() }()
	defer s.persistRunCancellation(sess, st)
	runTelemetry := telemetry.NewRun(sess.Dir, id, st.runID)
	defer runTelemetry.Close()
	var externalMeta map[string]string
	if len(external) > 0 {
		externalMeta = cloneExternal(external[0])
	}
	st.mu.Lock()
	st.external = externalMeta
	st.mu.Unlock()
	if parentID != nil {
		if err := sess.SetLeaf(*parentID); err != nil {
			st.err = err
			return
		}
	}
	cfg := s.cfg
	if isQuestionSide(sess.Config) {
		if err := s.syncQuestionSideModel(sess); err != nil {
			st.err = err
			return
		}
		reqModel = ""
	}
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
		if err := sess.SetLeaf(*parentID); err != nil {
			st.err = err
			return
		}
	}
	if _, err := sess.RecoverInterruptedToolCalls(); err != nil {
		st.err = fmt.Errorf("recover interrupted tool calls: %w", err)
		return
	}
	_, info, ok := s.registry.FindModel(sess.Config.Provider, sess.Config.Model)
	if !ok {
		st.err = fmt.Errorf("model %q/%q is %w", sess.Config.Provider, sess.Config.Model, errModelUnavailable)
		return
	}
	s.rememberModel(provider.ModelRef{Provider: sess.Config.Provider, Model: sess.Config.Model})
	var liveKey string
	var liveCredential provider.Credential
	var liveModel provider.Model
	runStreamer := s.streamer
	if s.requireModelCredential {
		_, resolved, credential, resolveErr := s.registry.ResolveCredential(sess.Config.Provider, sess.Config.Model)
		if resolveErr != nil {
			st.err = resolveErr
			return
		}
		info = resolved
		liveModel = resolved
		liveCredential = credential
		liveKey = credential.APIKey
		if s.providerExtensions != nil && s.providerExtensions.HasProvider(liveModel.Provider) {
			liveCredential, resolveErr = s.providerExtensions.RefreshCredential(ctx, s.registry, liveModel.Provider, liveCredential)
			if resolveErr != nil {
				st.err = fmt.Errorf("refresh provider credential: %w", resolveErr)
				return
			}
		}
	}
	bindingCredential := provider.Credential{}
	if s.requireModelCredential {
		bindingCredential = liveCredential
	} else if credential, status, err := s.registry.Credential(info.Provider); err == nil && status.Configured {
		bindingCredential = credential
	}
	if err := s.sessionAdmissionError(id); err != nil {
		st.err = err
		return
	}
	jobs := s.processesFor(id)
	profile := toolProfile(info)
	sideConversation := isQuestionSide(sess.Config)
	if sideConversation && metadataString(sess.Config.Metadata, "sideInitialization") != "ready" {
		st.err = errors.New("side conversation initialization is incomplete")
		return
	}
	var snapshot resources.Snapshot
	if sideConversation {
		var snapshotErr error
		snapshot, snapshotErr = questionSideResources(sess)
		if snapshotErr != nil {
			st.err = snapshotErr
			return
		}
	} else {
		snapshot = s.resources.Load(sess.ID(), sess.Header.CWD)
		s.reportManifestErrors(sess.ID(), snapshot.Extensions)
	}
	var sideReadOps *questionSideReadOps
	if sideConversation {
		var rootErr error
		sideReadOps, rootErr = pinQuestionSideReadOps(questionSideRoots(sess))
		if rootErr != nil {
			st.err = rootErr
			return
		}
		defer sideReadOps.Close()
	}
	builtinSet := builtin.Set{
		CWD: sess.Header.CWD, Processes: jobs, Agent: s,
		AgentParentSessionID: sess.ID(),
		Shells:               s.shells, Mutations: s.mutations,
		PathDirs: snapshot.PathDirs,
	}
	builtinSet.RequestUserInputAsync = func(callCtx context.Context, callID string, questions []session.QuestionInput) error {
		if err := callCtx.Err(); err != nil {
			return err
		}
		return s.requestQuestions(sess, callID, questions)
	}
	tls := builtinSet.Build(profile)
	if sideReadOps != nil {
		tls = builtin.Set{CWD: sess.Header.CWD, ReadOps: *sideReadOps}.Build(profile)
	}
	tg := toggles.Load(cfg.Home)
	tls = builtin.FilterBuiltins(tls, tg.Tools)
	enabledExtensions := extension.Enabled(snapshot.Extensions, tg.Extensions)
	var mcpTools []toolapi.Tool
	var mcpErr error
	if !sideConversation {
		s.ext.Configure(enabledExtensions)
		extTools := s.ext.Prepare(ctx, sess.ID(), sess.Header.CWD, enabledExtensions)
		tls = append(tls, extTools...)
		mcpSnapshot, err := s.resolveMCP(sess.Header.CWD)
		if err != nil {
			st.err = fmt.Errorf("MCP configuration: %w", err)
			return
		}
		mcpManager, releaseMCP, err := s.acquireMCP(mcpSnapshot, tg.MCP)
		if err != nil {
			st.err = err
			return
		}
		defer releaseMCP()
		if mcpManager != nil {
			mcpTools, mcpErr = mcpManager.Tools(ctx)
			if errors.Is(mcpErr, mcpruntime.ErrRequired) {
				st.err = fmt.Errorf("prepare required MCP server: %w", mcpErr)
				return
			}
			if mcpErr != nil {
				slog.Warn("MCP discovery incomplete", "session_id", id, "err", mcpErr)
			}
			tls = append(tls, mcpTools...)
		}
	}
	if ctx.Err() != nil {
		st.err = ctx.Err()
		return
	}
	tls = s.filterActiveTools(id, tls)
	tls = restrictQuestionSideTools(sess, tls)
	nestedTools := slices.Clone(tls)
	mcpNames := make(map[string]bool, len(mcpTools))
	for _, t := range mcpTools {
		mcpNames[toolapi.MustCanonical(t.Name())] = true
	}
	var deferredMCP []toolapi.Tool
	for _, t := range nestedTools {
		if mcpNames[toolapi.MustCanonical(t.Name())] {
			deferredMCP = append(deferredMCP, t)
		}
	}
	var searchCatalog *discovery.Catalog
	var searchTools []toolapi.Tool
	if len(deferredMCP) > 0 {
		candidate := discovery.New(deferredMCP)
		searchTools = s.filterActiveTools(id, builtin.FilterBuiltins([]toolapi.Tool{candidate.SearchTool()}, tg.Tools))
		if len(searchTools) > 0 {
			searchCatalog = candidate
		}
	}
	deferredNames := make(map[string]bool)
	if searchCatalog != nil {
		for _, name := range searchCatalog.Names() {
			deferredNames[name] = true
		}
	}
	executionTools := slices.Clone(nestedTools)
	var codeDispatcher *loop.ToolDispatcher
	var codeSession *codemode.Session
	var codeSessionMu sync.Mutex
	defer func() {
		codeSessionMu.Lock()
		session := codeSession
		codeSessionMu.Unlock()
		if cleanupErr := session.TerminateAll(context.Background()); cleanupErr != nil {
			st.err = errors.Join(st.err, fmt.Errorf("stop code mode cells: %w", cleanupErr))
		}
	}()
	getCodeSession := func() (*codemode.Session, error) {
		codeSessionMu.Lock()
		defer codeSessionMu.Unlock()
		if codeSession == nil {
			var sessionErr error
			codeSession, sessionErr = s.codeModeFor(id)
			if sessionErr != nil {
				return nil, sessionErr
			}
		}
		return codeSession, nil
	}
	callbacks := codemode.Callbacks{
		Invoke: func(callCtx context.Context, invocation codemode.Invocation) (codemode.ToolResult, error) {
			if isQuestionTool(invocation.Name) {
				return codemode.ToolResult{}, errors.New("request_user_input_async must be called directly")
			}
			call := loop.NestedToolCall{ParentCallID: invocation.ParentCallID, CellID: invocation.CellID, Name: invocation.Name, Arguments: invocation.Arguments}
			if invocation.Freeform {
				call.Input = &invocation.Input
			}
			res, invokeErr := codeDispatcher.DispatchNested(callCtx, call)
			return codemode.ToolResult{Content: res.Content, Details: res.Details, IsError: res.IsError, Terminate: res.Terminate}, invokeErr
		},
		Notify: func(callCtx context.Context, notification codemode.Notification) error {
			if err := callCtx.Err(); err != nil {
				return err
			}
			return codeDispatcher.NotifyNested(notification.ParentCallID, notification.CellID, notification.Text)
		},
	}
	codeSet := codetools.Set{GetSession: getCodeSession, Callbacks: callbacks, Nested: nestedTools, Deferred: deferredNames, Freeform: info.SupportsFreeformExec()}
	codeTools := codeSet.Build()
	if sideConversation {
		codeTools = nil
	}
	codeTools = builtin.FilterBuiltins(codeTools, tg.Tools)
	executionTools = append(executionTools, codeTools...)
	executionTools = append(executionTools, searchTools...)
	modelTools := func(history []types.Message) []toolapi.Tool {
		remaining := make(map[string]bool, len(deferredNames))
		for name := range deferredNames {
			remaining[name] = true
		}
		if searchCatalog != nil {
			for _, t := range searchCatalog.Loaded(history) {
				delete(remaining, toolapi.MustCanonical(t.Name()))
			}
		}
		var visible []toolapi.Tool
		for _, t := range nestedTools {
			if !remaining[toolapi.MustCanonical(t.Name())] {
				visible = append(visible, t)
			}
		}
		promptSet := codeSet
		promptSet.Deferred = remaining
		visible = append(visible, builtin.FilterBuiltins(promptSet.Build(), tg.Tools)...)
		if sideConversation {
			visible = restrictQuestionSideTools(sess, visible)
		}
		return append(visible, searchTools...)
	}
	tls = modelTools(nil)
	var occ *extension.Occupy
	if !sideConversation {
		occ = s.ext.Occupy(id)
	}
	if s.requireModelCredential {
		if s.providerExtensions != nil && s.providerExtensions.HasProvider(liveModel.Provider) {
			runStreamer = s.providerExtensions.NewStreamer(liveModel, liveCredential)
		} else {
			runStreamer = provider.NewLiveModel(liveModel, liveKey, occ.HTTPDoer()).WithIdleTimeout(time.Duration(s.cfg.Streaming.IdleTimeoutSeconds) * time.Second)
			runStreamer = occ.WrapStreamer(runStreamer)
		}
	}
	promptInput := prompt.Input{Resources: snapshot, Tools: tls, Toggle: tg.Skills}
	sys := prompt.Build(promptInput)
	if sideConversation {
		sys += "\n\n" + questionSidePolicy
	}

	useServerCompaction := s.serverSideCompaction(info) && occ.HTTPDoer() == nil
	replayBinding := replayBindingForSession(sess, info, bindingCredential)
	requestBinding := replayBinding
	inlineBinding := providerBindingForProtocol(info, bindingCredential, info.Compaction.Inline)
	opaqueReplaySafe := occ.OpaqueReplaySafe()
	if !opaqueReplaySafe {
		replayBinding = types.ProviderBinding{}
		useServerCompaction = false
	}
	emitter := &runEmitter{
		s: s, ctx: ctx, id: id, sess: sess, st: st, info: info,
		idempotencyKey: idempotencyKey, serverSide: useServerCompaction,
		binding: inlineBinding, replayBinding: replayBinding,
	}
	emit := emitter.Emit
	if mcpErr != nil {
		if err := emit(loop.Event{Type: loop.ExtensionNotice, Server: "mcp", MessageText: "MCP discovery incomplete: " + mcpErr.Error(), Status: "warn"}); err != nil {
			st.err = err
			return
		}
	}

	if !useServerCompaction && s.shouldCompact(sess, info, replayBinding) {
		current := sess.ContextToLeaf(replayBinding)
		preflight := &loop.Request{
			SessionID: id, CacheKey: sess.CacheAffinityKey(), System: sys, Messages: current.Messages,
			Provider: info.Provider, Model: info.ID, API: info.API,
			ProviderBinding:         requestBinding,
			Tools:                   toolSpecs(modelTools(current.Messages)),
			MaxTokens:               info.MaxTokens,
			ThinkingEffort:          sess.Config.ThinkingEffort,
			ThinkingFormat:          info.Compat.ThinkingFormat,
			MaxTokensField:          info.Compat.MaxTokensField,
			SupportsReasoningEffort: info.Compat.SupportsReasoningEffort,
			ForceAdaptiveThinking:   info.Compat.ForceAdaptiveThinking,
			ThinkingLevelMap:        info.ThinkingLevelMap,
		}
		if current.Responses != nil {
			preflight.ResponsesContext = current.Responses.Items
		}
		changed, compactErr := emitter.compactNow(compact.Intent{Reason: compact.ReasonPreflight}, preflight)
		if compactErr != nil && s.cfg.Compaction.Mode == "remote" &&
			!errors.Is(compactErr, compact.ErrNothingToCompact) {
			st.err = fmt.Errorf("remote preflight compaction: %w", compactErr)
			return
		}
		if changed && opaqueReplaySafe {
			runTelemetry.Reset("compaction")
			replayBinding = replayBindingForSession(sess, info, bindingCredential)
			requestBinding = replayBinding
			emitter.replayBinding = replayBinding
		}
	}

	hooks := s.composeHooks(sess, occ, info, replayBinding, useServerCompaction)
	if len(nextTurn) > 0 {
		hooks = s.withNextTurn(sess, st, hooks, nextTurn)
	}
	runCfg := loop.Config{
		Streamer:                runStreamer,
		RunID:                   st.runID,
		AgentID:                 st.agentTaskID,
		Generation:              st.agentGeneration,
		SessionID:               id,
		CacheKey:                sess.CacheAffinityKey(),
		Tools:                   executionTools,
		OutputStore:             s.outputStore,
		Telemetry:               runTelemetry,
		System:                  sys,
		Provider:                sess.Config.Provider,
		Model:                   sess.Config.Model,
		API:                     info.API,
		ProviderBinding:         requestBinding,
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
		CommitUserMessage:       s.commitUserMessage,
		Hooks:                   hooks,
	}
	if searchCatalog != nil {
		runCfg.ModelTools = modelTools
	}
	nestedCfg := runCfg
	nestedCfg.Tools = slices.DeleteFunc(slices.Clone(nestedTools), func(t toolapi.Tool) bool { return isQuestionTool(t.Name()) })
	codeDispatcher, err = loop.NewToolDispatcher(nestedCfg, emit)
	if err != nil {
		st.err = fmt.Errorf("prepare code mode dispatcher: %w", err)
		return
	}
	if useServerCompaction {
		runCfg.ResponsesCompactThreshold = s.remoteCompactThreshold(info)
		runCfg.ResponsesCompactFallback = s.cfg.Compaction.Mode == "auto"
	}
	runMessage := func(user types.Message) error {
		current := sess.ContextToLeaf(replayBinding)
		cfgForRun := runCfg
		if current.Responses != nil {
			cfgForRun.ResponsesContext = current.Responses.Items
		}
		_, runErr := loop.RunMessage(ctx, user, current.Messages, cfgForRun, emit)
		if runErr != nil {
			return fmt.Errorf("run message: %w", runErr)
		}
		return nil
	}
	user := types.Message{Role: "user", Content: content, Origin: origin, External: externalMeta, ClientRequestID: st.inputMetadata.ClientRequestID, Completion: st.inputMetadata.Completion, QuestionReply: st.inputMetadata.QuestionReply}
	if user.ClientRequestID == "" {
		user.ClientRequestID = st.runID + ":input"
	}
	s.restoreQuestionAnswers(sess, st)
	err = runMessage(user)
	for {
		st.mu.Lock()
		var steers []types.Message
		if !st.steerClosed && st.inbox != nil {
			steers = st.inbox.TakeWork()
		}
		if len(steers) == 0 {
			st.steerClosed = true
			st.mu.Unlock()
			break
		}
		st.mu.Unlock()
		for _, steer := range steers {
			if ctx.Err() != nil {
				if steer.QuestionReply != nil {
					continue
				}
				if _, aerr := s.commitUserMessage(steer, func() error {
					_, _, err := sess.AppendMessageWithKey(steer, "")
					return err
				}); aerr != nil {
					slog.Warn("persist undrained steer", "session_id", id, "err", aerr)
					st.inbox.Push(steer)
				}
				continue
			}
			err = runMessage(steer)
		}
		if ctx.Err() != nil {
			break
		}
	}
	st.err = err
}

func (s *Server) persistRunCancellation(sess *session.Session, st *runState) {
	reason, source := runCancellation(st)
	if reason == "" {
		return
	}
	details := map[string]any{"reason": reason}
	if source != "" {
		details["source"] = source
	}
	if st.runID != "" {
		details["runId"] = st.runID
	}
	if _, err := sess.AppendDetailsEvent(string(loop.RunAborted), details); err != nil {
		st.err = errors.Join(st.err, fmt.Errorf("persist run cancellation: %w", err))
	}
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

func (s *Server) composeHooks(sess *session.Session, occ *extension.Occupy, model provider.Model, binding types.ProviderBinding, serverSide bool) loop.Hooks {
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
		ShouldCompact: func() bool {
			return !serverSide && s.shouldCompact(sess, model, binding)
		},
		OnContextThreshold: func(ctx context.Context) (loop.CompactionResult, error) {
			outcome, err := s.compactSession(ctx, sess, compact.Intent{Reason: compact.ReasonThreshold}, nil)
			if errors.Is(err, compact.ErrNothingToCompact) {
				return loop.CompactionResult{Status: "empty"}, nil
			}
			if errors.Is(err, compact.ErrCompactionSkipped) {
				return loop.CompactionResult{Status: "cancelled"}, nil
			}
			if err != nil && !errors.Is(err, compact.ErrCompactionSkipped) {
				slog.Warn("threshold compact", "session_id", sess.ID(), "err", err)
			}
			return outcome.loopResult(), err
		},
		OnContextOverflow: func(ctx context.Context, failed loop.Request) (loop.CompactionResult, error) {
			outcome, err := s.compactSession(ctx, sess, compact.Intent{
				Reason: compact.ReasonOverflow, WillRetry: true,
			}, &failed)
			return outcome.loopResult(), err
		},
	}
}

func (s *Server) filterActiveTools(id string, tls []toolapi.Tool) []toolapi.Tool {
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
	var out []toolapi.Tool
	for _, t := range tls {
		if allow[toolapi.MustCanonical(t.Name())] {
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

func (s *Server) withNextTurn(sess *session.Session, st *runState, hooks loop.Hooks, items []session.ExtQueuedItem) loop.Hooks {
	inner := hooks.BeforeRun
	hooks.BeforeRun = func(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
		for _, item := range items {
			origin := ""
			if item.Extension != "" {
				origin = "extension:" + item.Extension
			}
			msg := types.Message{Role: "user", ClientRequestID: item.ID, Content: item.Content, Origin: origin, External: cloneExternal(item.External), Timestamp: time.Now().UnixMilli()}
			if e, _, aerr := sess.AppendMessageWithKey(msg, item.IdempotencyKey); aerr == nil {
				ev := loop.Event{Type: loop.MessageEnd, Message: &msg, EntryID: e.ID, ParentID: &e.ParentID}
				st.mu.Lock()
				st.appendLocked(&ev)
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

func (s *Server) liveSummarizer(ctx context.Context, sessionID, prov, model string) loop.Streamer {
	if !s.requireModelCredential {
		return s.streamer
	}
	_, resolved, credential, err := s.registry.ResolveCredential(prov, model)
	if err != nil {
		return s.streamer
	}
	if s.providerExtensions != nil && s.providerExtensions.HasProvider(prov) {
		credential, err = s.providerExtensions.RefreshCredential(ctx, s.registry, prov, credential)
		if err != nil {
			return s.streamer
		}
		return s.providerExtensions.NewStreamer(resolved, credential)
	}
	return provider.NewLiveModel(resolved, credential.APIKey, s.ext.HTTPDoer(sessionID)).WithIdleTimeout(time.Duration(s.cfg.Streaming.IdleTimeoutSeconds) * time.Second)
}

func (s *Server) liveCompactor(ctx context.Context, sessionID string, model provider.Model) (provider.Compactor, types.ProviderBinding) {
	if !s.requireModelCredential || model.Compaction.Standalone == "" {
		return nil, types.ProviderBinding{}
	}
	if s.providerExtensions != nil && s.providerExtensions.HasProvider(model.Provider) {
		_, resolved, credential, err := s.registry.ResolveCredential(model.Provider, model.ID)
		if err != nil {
			return nil, types.ProviderBinding{}
		}
		credential, err = s.providerExtensions.RefreshCredential(ctx, s.registry, model.Provider, credential)
		if err != nil {
			return nil, types.ProviderBinding{}
		}
		return s.providerExtensions.NewCompactor(resolved, credential),
			providerBindingForProtocol(resolved, credential, resolved.Compaction.Standalone)
	}
	if model.Compaction.Standalone != "openai" {
		return nil, types.ProviderBinding{}
	}
	_, resolved, credential, err := s.registry.ResolveCredential(model.Provider, model.ID)
	if err != nil {
		return nil, types.ProviderBinding{}
	}
	return provider.NewLiveModel(resolved, credential.APIKey, s.ext.HTTPDoer(sessionID)).
			WithIdleTimeout(time.Duration(s.cfg.Streaming.IdleTimeoutSeconds) * time.Second),
		providerBindingForProtocol(resolved, credential, resolved.Compaction.Standalone)
}

func (s *Server) summarizer(ctx context.Context, sessionID, cacheKey string, model provider.Model, effort string) compact.Summarizer {
	stream := s.liveSummarizer(ctx, sessionID, model.Provider, model.ID)
	return compact.StreamSummarizer{
		Stream: func(ctx context.Context, system, user string) (string, *types.Usage, error) {
			m, err := stream.Stream(ctx, loop.Request{
				Provider:                model.Provider,
				Model:                   model.ID,
				API:                     model.API,
				SessionID:               sessionID,
				CacheKey:                cacheKey,
				System:                  system,
				Messages:                []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: user}}}},
				ThinkingEffort:          effort,
				ThinkingLevelMap:        model.ThinkingLevelMap,
				ThinkingFormat:          model.Compat.ThinkingFormat,
				SupportsReasoningEffort: model.Compat.SupportsReasoningEffort,
				ForceAdaptiveThinking:   model.Compat.ForceAdaptiveThinking,
			}, func(loop.AssistantDelta) error { return nil })
			if err != nil {
				return "", nil, fmt.Errorf("stream summarizer: %w", err)
			}
			return m.Text(), m.Usage, nil
		},
	}
}

func (s *Server) providerBinding(model provider.Model) types.ProviderBinding {
	credential := provider.Credential{}
	if credential, status, err := s.registry.Credential(model.Provider); err == nil && status.Configured {
		return providerBindingWithCredential(model, credential)
	}
	return providerBindingWithCredential(model, credential)
}

func providerBindingWithCredential(model provider.Model, credential provider.Credential) types.ProviderBinding {
	protocol := model.Compaction.Standalone
	if protocol == "" {
		protocol = model.Compaction.Inline
	}
	if protocol == "" {
		protocol = "none"
	}
	return providerBindingForProtocol(model, credential, protocol)
}

func providerBindingForProtocol(model provider.Model, credential provider.Credential, protocol string) types.ProviderBinding {
	fingerprint := ""
	if credential.Type != "" || credential.APIKey != "" || len(credential.Value) > 0 {
		fingerprint = provider.CredentialFingerprint(credential)
	}
	return types.ProviderBinding{
		Provider:   model.Provider,
		API:        model.API,
		BaseURL:    model.BaseURL,
		Model:      model.ID,
		Credential: fingerprint,
		Compaction: protocol,
	}
}

func replayBindingForSession(sess *session.Session, model provider.Model, credential provider.Credential) types.ProviderBinding {
	protocols := []string{model.Compaction.Standalone, model.Compaction.Inline}
	var preferred types.ProviderBinding
	for _, protocol := range protocols {
		if protocol == "" {
			continue
		}
		binding := providerBindingForProtocol(model, credential, protocol)
		if preferred.Provider == "" {
			preferred = binding
		}
		if sess.ContextToLeaf(binding).Responses != nil {
			return binding
		}
	}
	if preferred.Provider != "" {
		return preferred
	}
	return providerBindingWithCredential(model, credential)
}

func toolSpecs(tools []toolapi.Tool) []toolapi.Spec {
	out := make([]toolapi.Spec, 0, len(tools))
	for _, tool := range tools {
		if provider, ok := tool.(toolapi.SpecProvider); ok {
			out = append(out, provider.ToolSpec())
			continue
		}
		out = append(out, toolapi.Spec{
			Type: "function", Name: tool.Name(),
			Description: tool.Description() + "\n\n" + tool.Prompt(),
			Parameters:  tool.Parameters(),
		})
	}
	return out
}

func requestToolSpecs(tools []session.ToolSchema) []toolapi.Spec {
	out := make([]toolapi.Spec, 0, len(tools))
	for _, tool := range tools {
		spec := toolapi.Spec{
			Type: tool.Type, Name: tool.Name, Description: tool.Description,
			Parameters: tool.Parameters,
		}
		if tool.Format != nil {
			spec.Format = &toolapi.Format{
				Type: tool.Format.Type, Syntax: tool.Format.Syntax, Definition: tool.Format.Definition,
			}
		}
		out = append(out, spec)
	}
	return out
}

func (s *Server) serverSideCompaction(model provider.Model) bool {
	supportsWire := model.API == "responses" ||
		(s.providerExtensions != nil && s.providerExtensions.HasProvider(model.Provider))
	return s.requireModelCredential &&
		s.cfg.Compaction.Enabled &&
		s.cfg.Compaction.ServerSide &&
		s.cfg.Compaction.Mode != "local" &&
		model.Compaction.Inline == "openai" &&
		supportsWire
}

func (s *Server) effectiveContextWindow(info provider.Model) int {
	window := info.ContextWindow
	if window <= 0 {
		return window
	}
	if maxContext := s.cfg.Compaction.MaxContextTokens; maxContext > 0 {
		window = min(window, maxContext)
	}
	return min(window, toggles.Load(s.cfg.Home).Message.ContextWindowLimit())
}

func (s *Server) remoteCompactThreshold(model provider.Model) int {
	window := s.effectiveContextWindow(model)
	threshold := window - s.cfg.Compaction.ReserveTokens
	if threshold < 1 {
		return 1
	}
	return threshold
}

func (s *Server) localSummaryInputLimit(model provider.Model) int {
	window := model.ContextWindow
	if window <= 0 {
		window = 128000
	}
	limit := window - s.cfg.Compaction.ReserveTokens
	if limit < 1 {
		limit = max(1, window/2)
	}
	return limit
}

type nonRetryableCompactionError interface {
	NonRetryable() bool
}

func compactRemoteWithRetry(ctx context.Context, compactor provider.Compactor, request loop.Request) (provider.CompactResult, error) {
	var last error
	for attempt := 0; attempt <= 5; attempt++ {
		if attempt > 0 {
			delay := 2 * time.Second * time.Duration(1<<uint(attempt-1))
			select {
			case <-ctx.Done():
				return provider.CompactResult{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		result, err := compactor.Compact(ctx, request)
		if err == nil {
			return result, nil
		}
		last = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return provider.CompactResult{}, err
		}
		var deterministic nonRetryableCompactionError
		if errors.As(err, &deterministic) && deterministic.NonRetryable() {
			return provider.CompactResult{}, err
		}
	}
	return provider.CompactResult{}, last
}

func (s *Server) shouldCompact(sess *session.Session, model provider.Model, binding types.ProviderBinding) bool {
	modelContext := sess.ContextToLeaf(binding)
	return compact.ShouldRun(compact.EstimateModelContext(modelContext, sess.LastCompactionAt()), s.effectiveContextWindow(model), s.cfg.Compaction)
}

var errRemoteCompactionUnavailable = errors.New("remote compaction is unavailable")

type compactionOutcome struct {
	Context          types.ModelContext
	Entry            session.Entry
	Strategy         string
	FromExtension    bool
	FirstKeptEntryID string
	TokensBefore     int
	Usage            *types.Usage
}

func (o compactionOutcome) loopResult() loop.CompactionResult {
	return loop.CompactionResult{
		Compacted: o.Entry.ID != "",
		Context:   o.Context, EntryID: o.Entry.ID, Strategy: o.Strategy,
		FromExtension: o.FromExtension, FirstKeptEntryID: o.FirstKeptEntryID,
		TokensBefore: o.TokensBefore, Usage: o.Usage,
	}
}

type remoteCompactionPlan struct {
	sourceLeafID   string
	request        loop.Request
	binding        types.ProviderBinding
	compactor      provider.Compactor
	tokensBefore   int
	contextChanged bool
}

func extensionPreparation(strategy string, prep *compact.Preparation, sess *session.Session) extension.CompactPreparation {
	if prep != nil {
		return extension.CompactPreparation{
			Strategy: strategy, SourceLeafID: prep.SourceLeafID,
			FirstKeptEntryID:    prep.FirstKeptEntryID,
			MessagesToSummarize: prep.MessagesToSummarize,
			TurnPrefixMessages:  prep.TurnPrefixMessages,
			RetainedTail:        prep.RetainedTail, IsSplitTurn: prep.IsSplitTurn,
			TokensBefore: prep.TokensBefore, PreviousSummary: prep.PreviousSummary,
		}
	}
	messages := sess.MessagesToLeaf()
	return extension.CompactPreparation{
		Strategy: strategy, SourceLeafID: sess.LeafID(),
		MessagesToSummarize: messages,
		TokensBefore:        compact.EstimateTokens(messages, sess.LastCompactionAt()),
	}
}

func customCompactionResult(in *extension.CustomCompactResult) compact.Result {
	if in == nil {
		return compact.Result{}
	}
	details := map[string]any{"fromExtension": true}
	if in.Details != nil {
		details["extension"] = in.Details
	}
	return compact.Result{
		Summary: in.Summary, Usage: in.Usage, Details: details,
		FromExtension: true,
	}
}

func (s *Server) prepareRemoteCompaction(ctx context.Context, sess *session.Session, model provider.Model, failed *loop.Request) (*remoteCompactionPlan, error) {
	if s.ext != nil && s.ext.HTTPDoer(sess.ID()) != nil {
		return nil, fmt.Errorf("%w with provider header interception", errRemoteCompactionUnavailable)
	}
	compactor, binding := s.liveCompactor(ctx, sess.ID(), model)
	if compactor == nil {
		return nil, errRemoteCompactionUnavailable
	}
	if failed != nil && failed.ProviderBinding.Provider != "" &&
		!failed.ProviderBinding.SameScope(binding) {
		return nil, errors.New("provider credential or compaction scope changed during remote compaction")
	}

	req := loop.Request{
		SessionID: sess.ID(), CacheKey: sess.CacheAffinityKey(), Provider: model.Provider, Model: model.ID, API: model.API,
		ProviderBinding:         binding,
		MaxTokens:               model.MaxTokens,
		ThinkingEffort:          sess.Config.ThinkingEffort,
		ThinkingFormat:          model.Compat.ThinkingFormat,
		MaxTokensField:          model.Compat.MaxTokensField,
		SupportsReasoningEffort: model.Compat.SupportsReasoningEffort,
		ForceAdaptiveThinking:   model.Compat.ForceAdaptiveThinking,
		ThinkingLevelMap:        model.ThinkingLevelMap,
	}
	if failed != nil {
		req = *failed
	}
	opaqueReplaySafe := isQuestionSide(sess.Config) || s.ext == nil || s.ext.Occupy(sess.ID()).OpaqueReplaySafe()
	reuseRequestContext := failed != nil &&
		opaqueReplaySafe &&
		failed.ProviderBinding.Equal(binding)
	portableExpanded := false
	if !reuseRequestContext {
		inputBinding := binding
		if !opaqueReplaySafe {
			inputBinding = types.ProviderBinding{}
		}
		current := sess.ContextToLeaf(inputBinding)
		portableExpanded = current.Portable
		req.Messages = current.Messages
		req.ResponsesContext = nil
		if current.Responses != nil {
			req.ResponsesContext = current.Responses.Items
		}
		req.ContextTransformed = false
		if req.System == "" || len(req.Tools) == 0 {
			system, schemas, found := sess.LastRequestHeader()
			if found {
				if req.System == "" {
					req.System = system
				}
				if len(req.Tools) == 0 {
					req.Tools = requestToolSpecs(schemas)
				}
			}
		}
		req.ProviderBinding = binding
	}
	providerFacingDigest := func(messages []types.Message) [32]byte {
		raw, _ := json.Marshal(messages)
		return sha256.Sum256(raw)
	}
	digest := providerFacingDigest(req.Messages)
	contextChanged := false
	if !req.ContextTransformed && s.ext != nil && !isQuestionSide(sess.Config) {
		hooks := s.ext.Hooks(sess.ID())
		if hooks.BeforeRun != nil {
			system, messages, err := hooks.BeforeRun(ctx, req.System, req.Messages)
			if err != nil {
				return nil, fmt.Errorf("transform compaction run: %w", err)
			}
			req.System, req.Messages = system, messages
			contextChanged = providerFacingDigest(req.Messages) != digest
			digest = providerFacingDigest(req.Messages)
		}
	}
	messages, err := materializeAttachments(ctx, req.Messages)
	if err != nil {
		return nil, fmt.Errorf("materialize compaction context: %w", err)
	}
	req.Messages = messages
	digest = providerFacingDigest(req.Messages)
	if !req.ContextTransformed && s.ext != nil && !isQuestionSide(sess.Config) {
		hooks := s.ext.Hooks(sess.ID())
		if hooks.TransformContext != nil {
			transformed, transformErr := hooks.TransformContext(ctx, req.Messages)
			if transformErr != nil {
				return nil, fmt.Errorf("transform compaction context: %w", transformErr)
			}
			req.Messages = transformed
			contextChanged = contextChanged || providerFacingDigest(req.Messages) != digest
			digest = providerFacingDigest(req.Messages)
		}
		req.ContextTransformed = true
	}
	if s.ext != nil && !isQuestionSide(sess.Config) {
		transformed, transformErr := s.ext.Occupy(sess.ID()).TransformProviderRequest(ctx, req)
		if transformErr != nil {
			return nil, fmt.Errorf("transform remote compaction request: %w", transformErr)
		}
		req = transformed
		contextChanged = contextChanged || providerFacingDigest(req.Messages) != digest
	}
	if req.Provider != model.Provider || req.Model != model.ID {
		if contextChanged {
			return nil, errors.New("cannot fall back to untransformed local compaction context")
		}
		return nil, fmt.Errorf("%w after provider/model request mutation", errRemoteCompactionUnavailable)
	}
	if len(req.ResponsesContext) == 0 && len(req.Messages) == 0 {
		return nil, errRemoteCompactionUnavailable
	}
	tokensBefore := compact.EstimateTokens(req.Messages, sess.LastCompactionAt())
	if portableExpanded {
		tokensBefore = compact.EstimateModelContext(types.ModelContext{Messages: req.Messages, Portable: true}, sess.LastCompactionAt())
	}
	return &remoteCompactionPlan{
		sourceLeafID: sess.LeafID(), request: req, binding: binding,
		compactor:      compactor,
		tokensBefore:   tokensBefore,
		contextChanged: contextChanged,
	}, nil
}

func (s *Server) compactSession(ctx context.Context, sess *session.Session, intent compact.Intent, request *loop.Request) (compactionOutcome, error) {
	_, model, ok := s.registry.FindModel(sess.Config.Provider, sess.Config.Model)
	if !ok {
		return compactionOutcome{}, fmt.Errorf("resolve compaction model %s/%s", sess.Config.Provider, sess.Config.Model)
	}
	binding := s.providerBinding(model)
	if s.cfg.Compaction.Mode == "remote" {
		if model.Compaction.Standalone == "" {
			return compactionOutcome{}, fmt.Errorf("model %s/%s does not support remote compaction", model.Provider, model.ID)
		}
		if strings.TrimSpace(intent.Instructions) != "" {
			return compactionOutcome{}, errors.New("remote compaction cannot use custom local instructions")
		}
	}

	localPrep, localPrepErr := compact.PrepareWithIntent(sess.LeafEntries(), s.cfg.Compaction, intent)
	if localPrepErr != nil && !errors.Is(localPrepErr, compact.ErrNothingToCompact) {
		return compactionOutcome{}, fmt.Errorf("prepare compaction: %w", localPrepErr)
	}
	useRemote := strings.TrimSpace(intent.Instructions) == "" &&
		s.cfg.Compaction.Mode != "local" &&
		model.Compaction.Standalone != ""
	strategy := "local"
	if useRemote {
		strategy = model.Compaction.Standalone
	}
	decision := extension.BeforeCompactDecision{}
	if s.ext != nil && !isQuestionSide(sess.Config) {
		decision = s.ext.BeforeCompact(ctx, sess.ID(), extension.BeforeCompactRequest{
			Reason: string(intent.Reason), WillRetry: intent.WillRetry,
			Instructions: intent.Instructions,
			Preparation:  extensionPreparation(strategy, localPrep, sess),
		})
	}
	if decision.Cancel {
		return compactionOutcome{}, compact.ErrCompactionSkipped
	}
	if decision.Result != nil {
		if s.cfg.Compaction.Mode == "remote" {
			return compactionOutcome{}, errors.New("remote compaction cannot use a custom local result")
		}
		useRemote = false
	}

	if useRemote {
		plan, prepareErr := s.prepareRemoteCompaction(ctx, sess, model, request)
		if prepareErr == nil {
			result, compactErr := compactRemoteWithRetry(ctx, plan.compactor, plan.request)
			if compactErr == nil {
				checkpoint := types.ResponsesContext{Binding: plan.binding, Items: result.Items}
				entry, appendErr := sess.AppendPreparedResponsesCompaction(
					plan.sourceLeafID, checkpoint, plan.tokensBefore, result.Usage,
				)
				if appendErr == nil {
					//nolint:contextcheck // reload/warmup is deferred to release and must outlive this compact ctx
					s.requestReload(sess.ID())
					return compactionOutcome{
						Context: sess.ContextToLeaf(plan.binding), Entry: entry,
						Strategy:     model.Compaction.Standalone,
						TokensBefore: plan.tokensBefore, Usage: result.Usage,
					}, nil
				}
				compactErr = fmt.Errorf("append remote compaction: %w", appendErr)
			}
			if errors.Is(compactErr, context.Canceled) || errors.Is(compactErr, context.DeadlineExceeded) {
				return compactionOutcome{}, compactErr
			}
			if plan.contextChanged {
				return compactionOutcome{}, fmt.Errorf("remote compaction failed after provider context transformation: %w", compactErr)
			}
			if s.cfg.Compaction.Mode == "remote" {
				return compactionOutcome{}, fmt.Errorf("execute remote compaction: %w", compactErr)
			}
			slog.Warn("remote compaction fallback", "session_id", sess.ID(), "provider", model.Provider, "model", model.ID, "err", compactErr)
		} else if !errors.Is(prepareErr, errRemoteCompactionUnavailable) {
			return compactionOutcome{}, prepareErr
		} else if s.cfg.Compaction.Mode == "remote" {
			return compactionOutcome{}, prepareErr
		} else {
			slog.Warn("remote compaction unavailable fallback", "session_id", sess.ID(), "err", prepareErr)
		}
	}

	if localPrepErr != nil {
		return compactionOutcome{}, localPrepErr
	}
	var result compact.Result
	var err error
	if decision.Result != nil {
		result = customCompactionResult(decision.Result)
	} else {
		result, err = compact.GenerateWithInputLimit(
			ctx, localPrep,
			s.summarizer(ctx, sess.ID(), sess.CacheAffinityKey(), model, sess.Config.ThinkingEffort),
			s.cfg.Compaction, s.localSummaryInputLimit(model),
		)
		if err != nil {
			return compactionOutcome{}, fmt.Errorf("execute compaction: %w", err)
		}
	}
	entry, err := compact.Commit(sess, localPrep, result)
	if err != nil {
		return compactionOutcome{}, err
	}
	//nolint:contextcheck // reload/warmup is deferred to release and must outlive this compact ctx
	s.requestReload(sess.ID())
	strategy = "local"
	if result.FromExtension {
		strategy = "extension"
	}
	return compactionOutcome{
		Context: sess.ContextToLeaf(binding), Entry: entry, Strategy: strategy,
		FromExtension:    result.FromExtension,
		FirstKeptEntryID: localPrep.FirstKeptEntryID,
		TokensBefore:     localPrep.TokensBefore, Usage: result.Usage,
	}, nil
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	s.pruneReplaysLocked(time.Now())
	st := s.runs[id]
	s.mu.Unlock()
	if st == nil {
		if _, err := s.sessionDir(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusGone, map[string]string{"error": "replay_unavailable", "recovery": "/v1/sessions/" + id})
		return
	}
	writer := newSSEWriter(w)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer writer.watch(ctx)()

	snapshot := s.replaySnapshot(id, r.URL.Query().Get("through"))
	write := writer.write
	stopPing := make(chan struct{})
	pingStopped := make(chan struct{})
	defer func() { cancel(); close(stopPing); <-pingStopped }()
	go func() {
		defer close(pingStopped)
		ticker := time.NewTicker(ssePingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if write(": ping\n\n") != nil {
					cancel()
					return
				}
			}
		}
	}()
	var encoder loop.MessageEncoder
	reader := &runReader{}
	st.mu.Lock()
	runID := st.runID
	since := parseCursor(r, runID)
	for _, ev := range st.evs {
		snapshot.observe(ev)
	}
	st.addReader(reader)
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.removeReader(reader)
		st.mu.Unlock()
	}()
	go func() {
		<-ctx.Done()
		st.mu.Lock()
		st.wait.Broadcast()
		st.mu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		st.mu.Lock()
		for reader.pos >= len(st.evs) {
			select {
			case <-st.done:
				st.mu.Unlock()
				return
			case <-ctx.Done():
				st.mu.Unlock()
				return
			default:
				st.wait.Wait()
			}
		}
		ev := st.evs[reader.pos]
		reader.pos++
		st.mu.Unlock()
		if ev.Blank || ev.Seq <= since || snapshot.covers(ev) {
			continue
		}
		if ev.Type == loop.AgentEnd {
			select {
			case <-st.done:
			case <-ctx.Done():
				return
			}
		}
		encodedAt := time.Now()
		b, err := json.Marshal(encoder.Encode(snapshot.frame(*ev)))
		encodeTime := time.Since(encodedAt)
		if err != nil {
			slog.Error("marshal SSE event", "err", err)
			return
		}
		writtenAt := time.Now()
		if write("id: %s:%d\nevent: %s\ndata: %s\n\n", runID, ev.Seq, ev.Type, b) != nil {
			return
		}
		writeTime := time.Since(writtenAt)
		if ev.Seq%128 == 0 || encodeTime+writeTime >= 100*time.Millisecond {
			slog.Debug("stream frame", "run_id", runID, "seq", ev.Seq, "bytes", len(b), "queue_us", encodedAt.Sub(ev.BufferedAt).Microseconds(), "encode_us", encodeTime.Microseconds(), "write_us", writeTime.Microseconds())
		}
		if ev.Type == loop.AgentEnd {
			return
		}
	}
}

func parseCursor(r *http.Request, runID string) int64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("since"))
	}
	if raw == "" {
		return 0
	}
	run, seqPart, ok := strings.Cut(raw, ":")
	if ok && run != runID {
		return 0
	}
	if !ok {
		seqPart = run
	}
	seq, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil || seq < 0 {
		return 0
	}
	return seq
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Source    string `json:"source"`
		Scope     string `json:"scope"`
		ProcessID int64  `json:"session_id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, "invalid abort request", http.StatusBadRequest)
			return
		}
	}
	if body.Scope == "process" {
		s.mu.Lock()
		manager := s.processes[id]
		s.mu.Unlock()
		if manager == nil {
			http.Error(w, "no process manager for session", http.StatusNotFound)
			return
		}
		snapshot, err := manager.Terminate(body.ProcessID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, 200, map[string]any{"aborted": true, "process": snapshot})
		return
	}
	if body.Scope != "" && body.Scope != "turn" && body.Scope != "tree" {
		http.Error(w, "invalid abort scope", http.StatusBadRequest)
		return
	}
	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = "api"
	}
	s.mu.Lock()
	st := s.runs[id]
	s.mu.Unlock()
	if st != nil {
		s.cancelRun(id, st, cancelReasonUserRequest, source, true)
		st.mu.Lock()
		runID := st.runID
		st.mu.Unlock()
		slog.Info("run abort requested", "session_id", id, "run_id", runID, "reason", cancelReasonUserRequest, "source", source)
		if s.ext != nil {
			s.ext.CloseSession(id)
		}
	}
	if body.Scope == "tree" {
		s.stopRuntimeTree(id)
	}
	abortedAgent := false
	if s.agentTasks != nil {
		snapshots, err := s.agentTasks.StopSubtree(id)
		if err == nil {
			for _, snapshot := range snapshots {
				abortedAgent = abortedAgent || snapshot.Status == agent.Killed
			}
		}
	}
	s.cancelUIPrompts(id)
	writeJSON(w, 200, map[string]any{
		"aborted": st != nil || abortedAgent, "reason": cancelReasonUserRequest, "source": source,
	})
}

func (s *Server) doCompact(w http.ResponseWriter, r *http.Request, suppliedInstructions ...string) {
	id := r.PathValue("id")
	instructions := ""
	if len(suppliedInstructions) > 0 {
		instructions = strings.TrimSpace(suppliedInstructions[0])
	} else if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Instructions string `json:"instructions"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, "invalid compact request", http.StatusBadRequest)
			return
		}
		instructions = strings.TrimSpace(body.Instructions)
	}
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
	intent := compact.Intent{Reason: compact.ReasonManual, Instructions: instructions}
	if err := s.publishStandaloneCompactionEvent(ctx, sess, loop.Event{
		Type: loop.CompactionStart, Reason: string(intent.Reason),
	}); err != nil {
		//nolint:contextcheck // release may rewarm after the occupy ctx ends
		s.release(id, st)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outcome, err := s.compactSession(ctx, sess, intent, nil)
	eventErr := s.publishStandaloneCompactionEvent(ctx, sess, compactionEndEvent(intent, outcome, err))
	//nolint:contextcheck // release may rewarm after the occupy ctx ends
	s.release(id, st)
	if eventErr != nil {
		http.Error(w, eventErr.Error(), http.StatusInternalServerError)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "compact aborted", http.StatusConflict)
		return
	}
	if err != nil {
		if errors.Is(err, compact.ErrNothingToCompact) {
			http.Error(w, "nothing to compact (session too small)", http.StatusConflict)
			return
		}
		if errors.Is(err, compact.ErrCompactionSkipped) {
			http.Error(w, "compaction skipped by extension", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishContextUsage(sess)
	writeJSON(w, 200, map[string]any{
		"id": outcome.Entry.ID, "type": outcome.Entry.Type,
		"firstKeptEntryId": outcome.FirstKeptEntryID, "handled": true,
		"strategy": outcome.Strategy,
	})
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	gate := s.inputGate(r.PathValue("id"))
	gate.Lock()
	defer gate.Unlock()
	if err := s.sessionAdmissionError(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	var body struct {
		EntryID        string `json:"entryId"`
		ForkMode       string `json:"forkMode"`
		Purpose        string `json:"purpose"`
		QuestionItemID string `json:"questionItemId"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
	}
	if body.Purpose != "" && body.Purpose != questionSidePurpose {
		http.Error(w, "invalid fork purpose", http.StatusBadRequest)
		return
	}
	if body.Purpose == "" && body.EntryID == "" && s.running(r.PathValue("id")) {
		http.Error(w, "session busy", http.StatusConflict)
		return
	}
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	if body.Purpose == questionSidePurpose {
		if body.EntryID != "" || body.ForkMode != "" || body.QuestionItemID == "" {
			http.Error(w, "question side requires only purpose and questionItemId", http.StatusBadRequest)
			return
		}
		s.forkQuestionSide(w, r, sess, body.QuestionItemID)
		return
	}
	if isQuestionSide(sess.Config) {
		http.Error(w, "side conversations cannot create forks", http.StatusConflict)
		return
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
	//nolint:contextcheck // fork warmup is process-owned via runtimeCtx, not the HTTP request
	s.kickWarmup(dst.ID(), dst.Header.CWD)
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
}

func (s *Server) rememberModel(ref provider.ModelRef) {
	if err := s.registry.RememberDefault(ref); err != nil {
		slog.Warn("remember last model", "provider", ref.Provider, "model", ref.Model, "err", err)
	}
}

func (s *Server) resolveSessionModel(sess *session.Session, requested string) (provider.ModelRef, provider.Model, error) {
	if requested == "" {
		ref := provider.ModelRef{Provider: sess.Config.Provider, Model: sess.Config.Model}
		model, err := s.registry.ResolveRef(ref)
		return ref, model, err
	}
	return s.registry.ResolveSpec(requested, sess.Config.Provider)
}

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
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write JSON response", "err", err)
	}
}
