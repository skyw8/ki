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
	"ki/internal/command"
	"ki/internal/compact"
	"ki/internal/config"
	"ki/internal/extension"
	"ki/internal/idgen"
	"ki/internal/logging"
	"ki/internal/loop"
	"ki/internal/prompt"
	"ki/internal/provider"
	"ki/internal/push"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/telemetry"
	"ki/internal/toggles"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin"
	"ki/internal/tool/output"
	"ki/internal/types"
	"ki/internal/workspace"

	// Options constructs a Server.
	"ki/internal/process"
	filetools "ki/internal/tool/builtin/file"
)

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
	providerExtensions     *extension.ProviderManager
	requireModelCredential bool
	providerAuthMu         sync.Mutex
	providerAuth           map[string]*providerAuthState
	ext                    *extension.Manager
	resources              *resources.Loader
	mu                     sync.Mutex
	runs                   map[string]*runState
	replay                 replayCache
	processes              map[string]*process.Manager
	outputStore            *output.Store
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
	runtimeClosed          bool
}

const (
	browserSessionCookie = "ki_session"
	browserCSRFCookie    = "ki_csrf"
	browserSessionTTL    = 12 * time.Hour
)

type authSource uint8

const (
	authNone authSource = iota
	authBearer
	authBrowserSession
)

type runState struct {
	agentTaskID     string
	agentGeneration uint64
	inputMetadata   types.Message
	cancel          context.CancelFunc
	runID           string
	external        map[string]string
	// cancelReason/source are first-writer-wins diagnostics. The context API
	// collapses every caller to context.Canceled, so retain the initiating
	// boundary before invoking cancel.
	cancelReason string
	cancelSource string
	mu           sync.Mutex
	// evs holds pointers so a trimmed ("blank") slot collapses to one word plus
	// the shared blankEvent instead of a 424-byte loop.Event per chunk. A
	// long turn streams tens of thousands of chunks; their slots must not
	// outgrow the payloads the trimming just freed.
	evs   []*loop.Event
	wait  *sync.Cond
	done  chan struct{}
	err   error
	inbox *loop.Inbox
	// steerClosed closes the handoff window after loop.Run has returned. A
	// message that arrives after that point must become a queued/resumed run,
	// never a successful write to an Inbox that nobody will drain.
	steerClosed bool
	// seq numbers every buffered event; readers echo it back to resume.
	seq int64
	// promptKey is the system+tools digest of the last buffered request_header,
	// so a repeat can be stored as promptUnchanged instead of the full payload.
	promptKey string
	// pending holds the indices of buffered transient events (a message's start
	// and chunks, tool progress) still eligible for payload trimming; partial is
	// the one index that must survive it — the newest chunk of the message in
	// flight, which a reader attaching now still needs to render. readers is the
	// set of attached SSE readers by their next read index. Trimming needs all
	// three: a payload may go only once every reader has passed it and it is not
	// the in-flight partial.
	pending []int
	partial int
	readers map[*runReader]struct{}
}

// runReader is one attached SSE reader's next index into runState.evs.
// Guarded by the owning runState.mu.
type runReader struct{ pos int }

// blankEvent is every trimmed slot's payload. One shared value keeps a blanked
// chunk at one pointer per slot; readers skip it and it is never marshaled.
var blankEvent = &loop.Event{Blank: true}

// minReaderPos returns the smallest index no attached reader has consumed:
// everything below it has been delivered (or there is no reader, so nothing is
// owed). Must be called with mu held.
func (st *runState) minReaderPos() int {
	min := len(st.evs)
	for r := range st.readers {
		if r.pos < min {
			min = r.pos
		}
	}
	return min
}

// addReader and removeReader track attached SSE readers. Must be called with mu
// held. The map is created lazily so a runState literal (tests) needs no setup.
func (st *runState) addReader(r *runReader) {
	if st.readers == nil {
		st.readers = map[*runReader]struct{}{}
	}
	st.readers[r] = struct{}{}
}

func (st *runState) removeReader(r *runReader) { delete(st.readers, r) }

// transient reports whether a buffered event is superseded by a later one:
// streaming chunks carry the whole accumulated partial, a message_start is
// superseded by its own chunks, and tool progress is superseded by the next
// progress tick. Only the newest of a run of them can matter to a reader that
// arrives later — once the message is persisted even that one stops mattering
// (see appendLocked).
func transient(t loop.EventType) bool {
	return t == loop.MessageStart || t == loop.MessageUpdate || t == loop.ToolExecutionUpdate || t == loop.PatchApplyUpdated
}

// appendLocked buffers one event for replay and stamps it with the run's next
// sequence number.
//
// Why payload trimming lives here: the SSE replay exists for a client that
// attaches mid-run, and the transcript (GET /v1/sessions/{id}) already carries
// every persisted entry. Two payloads grow with the run's *output* rather than
// with its number of rounds, so storing them verbatim made the buffer quadratic
// in a long turn's streaming text and made every re-attach resend it:
//
//   - message_update / tool_execution_update repeat the whole accumulated
//     partial on every chunk, so all but the newest per message are redundant
//     (see trimLocked);
//   - request_header repeats the run's system prompt and tool schemas every
//     round, so the repeat is stored as promptUnchanged — the same shape the
//     session view already gives the persisted entries, which the WebUI already
//     folds back into the previous prompt body.
//
// The caller's event keeps its full payload: the stages after buffer
// (context-usage estimation, extension lifecycle) need it. Must be called with
// mu held.
func (st *runState) appendLocked(ev *loop.Event) {
	// Every buffered event carries the run identity, whichever path appended it
	// (the loop funnel, a queue drain, a sideband). A reader that attaches later
	// must be able to attribute what it replays, and a client builds its resume
	// cursor from these fields.
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
		// The previous partial loses its exemption; the new one becomes the
		// payload a reader attaching right now still needs.
		st.retirePartialLocked()
		st.partial = idx
		st.trimLocked()
	case ev.Type == loop.MessageEnd && stored.EntryID != "":
		// The message is on disk: its start and every chunk are in the
		// transcript this reader can fetch, so the group is no longer in-flight
		// state and the whole of it may be trimmed. A message_end without an
		// entry id (the append failed) is not on disk, so it stays.
		st.retirePartialLocked()
		st.trimLocked()
	}
}

// retirePartialLocked stops exempting the in-flight partial from trimming; its
// index joins pending so the next trim may drop it. Must be called with mu held.
func (st *runState) retirePartialLocked() {
	if st.partial >= 0 {
		st.pending = append(st.pending, st.partial)
		st.partial = -1
	}
}

// Bounds on the superseded streaming payloads a run keeps for a reader that
// attaches later.
//
// Why a bound exists at all: trimming used to be gated purely on reader
// positions ('never drop a chunk a reader has not read yet'), and one stalled
// client — a backgrounded tab, a suspended phone page, a port-forward that
// stopped reading — then pinned the whole buffer. A live run in the wild held
// 14,766 events / 286 MB of superseded message_update payloads and left the
// daemon at ~1 GB RSS, and every fresh page load replayed all of it (the
// stall's send queue measured 3.1 MB) before it could render anything.
//
// Why dropping them is safe: a message_update carries the whole accumulated
// partial, so the newest one per message is all a reader needs, and everything
// older than that is on disk in the transcript the same reader can fetch. A
// client that falls this far behind cannot be shown a live stream anyway; the
// CLI prints the accumulated partial and the missing suffix instead of the raw
// increments (see internal/cli streamPrinter).
const (
	maxKeptSuperseded      = 128
	maxKeptSupersededBytes = 4 << 20
)

// trimLocked blanks superseded payloads, keeping memory proportional to the
// in-flight partial instead of to the whole run's streamed output.
//
// The in-flight partial always stays: a reader attaching right now still gets
// one chunk to render, and the next live chunk carries the whole accumulated
// message anyway. Everything else goes once every attached reader has passed it,
// or — for a reader that is further behind than the caps above — once it is
// older than the newest maxKeptSuperseded payloads. Must be called with mu held.
func (st *runState) trimLocked() {
	limit := st.minReaderPos()
	// Pass one, newest first: the retention caps keep the payloads a client
	// attaching now can still use, and mark everything else blank.
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
	// Pass two compacts in place, oldest first. The write cursor stays behind the
	// read cursor because pending is scanned in the same order it was built.
	out := st.pending[:0]
	for _, idx := range st.pending {
		if st.evs[idx] == blankEvent {
			continue
		}
		out = append(out, idx)
	}
	st.pending = out
}

// eventPayloadBytes approximates what a buffered event retains, for the
// retention cap. The exact size needs a marshal, and the cap only has to be
// right about the order of magnitude.
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

// promptDigest identifies a request_header payload (system prompt + tool
// schemas) so a repeat can be stored without its body.
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

// File is ~/.ki/server.json
type File struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// New builds a server (does not listen).
func New(opt Options) (*Server, error) {
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
	sidx := session.NewIndex(infos) // reuse the List walk: zero extra reads
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
		streamer:               st,
		registry:               reg,
		providerExtensions:     providerExtensions,
		requireModelCredential: requireCredential,
		providerAuth:           map[string]*providerAuthState{},
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
		runtimeCtx:             runtimeCtx,
		runtimeCancel:          runtimeCancel,
	}
	srv.replay = newReplayCache()
	srv.ext = extension.NewManager(opt.Config.Home, srv.onExtensionError)
	srv.ext.SetHost(srv)
	// The runtime state is part of GET /v1/extensions; push a refetch hint on
	// every transition instead of making the WebUI poll that endpoint.
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
	srv.restoreAgentTasks(infos)

	// Web Push is best-effort infrastructure: a key or registry that cannot be
	// read only disables the off-page channel, never the server. The WebUI falls
	// back to its live-tab notification when push reports unavailable.
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

// Token is the bearer secret.
func (s *Server) Token() string { return s.token }

// Reload drops idle sessions' resource snapshots and extension views so the
// next prompt/GET rebuilds them. Occupied sessions are queued onto pendingReload
// and applied at release. Per-session invalidation is reloadSession.
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

// startExtensions discovers the enabled global catalog and launches all
// executable extension runtimes. It is called after the listener exists and
// again during a global reload; declarative extensions remain process-free.
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

// reloadSession invalidates one idle session. A live run keeps its fixed tool
// header and connection; requestReload records pendingReload, and release
// applies it after occupy ends (prompt defer or compact).
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

// publishStandaloneCompactionEvent gives manual compaction the same durable
// and extension-visible lifecycle as run-owned compaction events.
func (s *Server) publishStandaloneCompactionEvent(ctx context.Context, sess *session.Session, ev loop.Event) error {
	if err := persistCompactionEvent(sess, &ev); err != nil {
		return fmt.Errorf("append manual compaction event: %w", err)
	}
	s.publishPush(sess.ID(), ev)
	if s.ext != nil {
		s.ext.OnEvent(ctx, sess.ID(), extension.RedactEvent(ev, sess.ID()))
	}
	return nil
}

// publishContextUsage recomputes and persists the model-facing context size
// after a compaction that ran outside a request (manual /compact, threshold
// auto-compaction), then pushes the value so the WebUI context meter updates
// immediately instead of waiting for the next prompt's request_header.
//
// The estimate is char/4: the newest assistant usage predates the compaction,
// so EstimateTokens falls back and contextUsageEstimate adds the system prompt
// and tool schemas from the persisted last request header.
func (s *Server) publishContextUsage(sess *session.Session) {
	_, info, ok := s.registry.FindModel(sess.Config.Provider, sess.Config.Model)
	if !ok {
		return
	}
	// No live request: the compaction already ran, so the schemas come from the
	// last persisted request_header.
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

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
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
	api.HandleFunc("PATCH /v1/providers/{id}/models", s.auth(s.patchProviderModel))
	api.HandleFunc("DELETE /v1/providers/{id}/models", s.auth(s.deleteProviderModel))
	api.HandleFunc("PUT /v1/default-model", s.auth(s.putDefaultModel))
	api.HandleFunc("GET /v1/meta", s.auth(s.meta))
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
	// gzip sits outside recoverHTTP so panics recovered there are still written
	// through the compressing writer, and every route (SPA assets and /v1 JSON)
	// shares one compression path.
	return gzipHandler(recoverHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			api.ServeHTTP(w, r)
			return
		}
		s.serveUI(w, r)
	})))
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
		source := s.authenticate(r)
		if source == authNone {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if source == authBrowserSession {
			// Idle-based expiry: a tab left open across a working day keeps
			// refreshing its window instead of dying at a fixed 12h mark.
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
	if cookie, err := r.Cookie(browserSessionCookie); err == nil && s.validBrowserSession(cookie.Value) {
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

// renewBrowserSession extends a browser session once more than half its TTL has
// been used, and rewrites both cookies to match. Sliding expiry keeps a tab
// that is actively in use (a phone left on the WebUI across a day) signed in,
// while an abandoned session still ages out. Rewriting on every request would
// be needless churn, hence the half-TTL threshold.
func (s *Server) renewBrowserSession(w http.ResponseWriter, r *http.Request) {
	sessionCookie, err := r.Cookie(browserSessionCookie)
	if err != nil {
		return
	}
	csrfCookie, err := r.Cookie(browserCSRFCookie)
	if err != nil {
		// The pair is written together at login; without the CSRF half a
		// rewrite would break unsafe methods, so let this session expire.
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
	setBrowserCookies(w, sessionCookie.Value, csrfCookie.Value, now, requestIsSecure(r))
}

// setBrowserCookies writes the session/CSRF cookie pair with one shared expiry,
// so login and renewal cannot drift apart.
func setBrowserCookies(w http.ResponseWriter, sessionID, csrf string, now time.Time, secure bool) {
	expires := now.Add(browserSessionTTL)
	maxAge := int(browserSessionTTL / time.Second)
	http.SetCookie(w, &http.Cookie{
		Name:     browserSessionCookie,
		Value:    sessionID,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     browserCSRFCookie,
		Value:    csrf,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(browserCSRFCookie)
	if err != nil {
		return false
	}
	return sameSecret(r.Header.Get("X-Ki-CSRF"), cookie.Value)
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.authenticate(r) != authNone})
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

	setBrowserCookies(w, sessionID, csrf, now, requestIsSecure(r))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(browserSessionCookie); err == nil {
		s.mu.Lock()
		delete(s.browserSessions, cookie.Value)
		s.mu.Unlock()
	}
	secure := requestIsSecure(r)
	for _, name := range []string{browserSessionCookie, browserCSRFCookie} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == browserSessionCookie,
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
	if err := WriteServerFile(s.cfg.Home, File{Addr: dialAddress(ln.Addr()), Token: s.token}); err != nil {
		slog.Warn("server.json", "err", err)
	}
	slog.Info("listen", "addr", ln.Addr().String())
	s.startExtensions()
	return httpSrv.Serve(ln)
}

func dialAddress(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	// Why: wildcard listeners are valid bind addresses but not reliable client
	// destinations (and [::] may be sent through an HTTP proxy). Persist a
	// loopback endpoint so CLI actions reuse the live server instead of starting
	// a second runtime and duplicate extension sidecars.
	if host == "" || net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
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

// Shutdown stops the HTTP server and every extension sidecar.
func (s *Server) Shutdown(ctx context.Context) error {
	s.runtimeMu.Lock()
	s.runtimeClosed = true
	runtimeCancel := s.runtimeCancel
	s.runtimeMu.Unlock()
	if runtimeCancel != nil {
		runtimeCancel()
	}
	// Why: session creation starts warmup asynchronously, and its failure
	// events still write to the session directory. Wait before cleanup can
	// remove that directory underneath a warmup goroutine.
	s.runtimeWG.Wait()
	if s.agentTasks != nil {
		s.agentTasks.Close()
	}
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
	jobs := make([]*process.Manager, 0, len(s.processes))
	for id, store := range s.processes {
		jobs = append(jobs, store)
		delete(s.processes, id)
	}
	s.mu.Unlock()
	for _, store := range jobs {
		store.Close()
	}
	if s.agentTasks != nil {
		s.agentTasks.Close()
	}
	// Why: release → dispatchQueue can spawn a run after a one-shot snapshot;
	// drain until idle (or ctx done) so TempDir cleanup is not racing writers.
	for {
		s.mu.Lock()
		active := make([]*runState, 0, len(s.runs))
		for _, st := range s.runs {
			if st == nil {
				continue
			}
			select {
			case <-st.done:
			default:
				s.cancelRun("", st, cancelReasonServerShutdown, "server", false)
				active = append(active, st)
			}
		}
		s.mu.Unlock()
		if len(active) == 0 {
			break
		}
		stopped := false
		for _, st := range active {
			select {
			case <-st.done:
			case <-ctx.Done():
				stopped = true
			case <-time.After(2 * time.Second):
			}
		}
		if stopped || ctx.Err() != nil {
			break
		}
	}
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
		return nil
	}
	return httpSrv.Shutdown(ctx)
}

func (s *Server) processesFor(id string) *process.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if s.outputStore != nil {
		_ = s.outputStore.CloseSession(id)
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
		CWD            string         `json:"cwd"`
		WorkspaceID    string         `json:"workspaceId"`
		Model          string         `json:"model"`
		ThinkingEffort string         `json:"thinkingEffort"`
		Metadata       map[string]any `json:"metadata"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	rec, err := s.resolveWorkspace(body.WorkspaceID, body.CWD, "")
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

// get answers the WebUI session view.
//
// The default response is the conversation tail: the newest leaf entries plus
// the cursor for older pages. The full-tree index is opt-in via fields=index
// (the trajectory table, branch navigation), and fields=runtime answers the
// readiness poll without reading the transcript at all. Why: first paint used
// to carry an index of every entry, so opening a long session waited for the
// whole file to be parsed and for megabytes of JSON, none of which the newest
// messages needed.
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
	runtimeOnly := hasField(fields, "runtime") && !withIndex && entryID == "" && batch == "" && before == "" && turnID == ""
	full := entryID != "" || batch != "" || before != "" || withIndex || compact || traceView || inspectView || turnID != ""

	var snap *sessionSnap
	var err error
	if runtimeOnly && !traceView && !inspectView {
		snap, err = s.loadSessionSnap(id, false, 0)
	} else if full && !traceView && !inspectView {
		snap, err = s.loadIndexedSessionSnap(id)
	} else if full {
		snap, err = s.loadSessionSnap(id, true, 0)
	} else {
		snap, err = s.loadSessionSnap(id, false, limit)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
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
		writeJSON(w, 200, map[string]any{"entry": got[0]})
		return
	}
	if batch != "" {
		got, err := snap.transcript.Lookup(strings.Split(batch, ","))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, 200, map[string]any{"entries": got})
		return
	}
	// A stale cursor after a branch change is not evidence of reaching the
	// root. Returning an empty successful page would permanently hide older
	// history until the browser is reloaded.
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
			writeJSON(w, 200, page)
			return
		}
		view, _, err := snap.transcript.Tail(snap.leafID, before, "", limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, 200, map[string]any{
			"entries":  view.Entries,
			"hasMore":  view.HasMore,
			"oldestId": view.OldestID,
		})
		return
	}
	if withIndex && !hasField(fields, "runtime") {
		// Index consumers already have a tail. Repeating it here doubled the
		// weak-link transfer and could overwrite hydrated bodies in the UI.
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
	if runtimeOnly {
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
		runtime["entries"], runtime["hasMore"], runtime["oldestId"] = tail.Entries, tail.HasMore, tail.OldestID
	}
	if withIndex || (!compact && snap.complete && snap.small) {
		// A session smaller than one tail read was read in full anyway, so its
		// index costs nothing extra and the client needs no second request; a
		// long one stays opt-in.
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

// hasField reports whether a comma-separated fields query contains name.
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
	// Why: ready is set after Prepare, so read ready first then RuntimeCommands
	// to avoid ready:true with an empty slash catalog.
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
			"compaction":         m.Compaction,
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
	modelSettingsRequested := body.Model != "" || body.ThinkingEffort != nil
	selectionChanged := body.Model != "" || body.ThinkingEffort != nil || body.LeafID != nil
	var modelRef provider.ModelRef
	var modelEffort string
	if modelSettingsRequested {
		spec := cmp.Or(body.Model, sess.Config.Model)
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
		// A remote checkpoint is model-bound. Recompute after the complete patch
		// (including a possible branch change) so the UI never keeps displaying
		// usage measured against an incompatible opaque prefix.
		s.publishContextUsage(sess)
	}
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
	writeJSON(w, 200, s.sessionMap(sess, nil))
}

// list renders the session sidebar. Rows come from the stamp-validated
// ListCache, and the response carries an ETag over the rendered body so a
// refresh that finds nothing changed returns 304 instead of a new array.
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	infos, err := s.slist.List(s.cfg.Sessions.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	activity := s.sessionActivity(infos)
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		out = append(out, s.infoMap(info, activity))
	}
	body, err := json.Marshal(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Why hash the body instead of the file stamps: the row also carries
	// in-memory state (running) and the workspace mapping, so anything derived
	// from those must invalidate the tag too. Hashing the exact bytes keeps the
	// tag correct for every display-affecting input without tracking them.
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

// mutateIdleSession closes the check-then-mutate race with occupy. Model
// changes append a model_change entry and therefore move the active leaf just
// like an explicit branch change; accepting either while a run or standalone
// compaction owns the session can invalidate its prepared commit.
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
		Text            string          `json:"text"`
		Content         []types.Content `json:"content"`
		ParentID        *string         `json:"parentId"`
		Model           string          `json:"model"`
		Delivery        string          `json:"delivery"`
		QueueID         string          `json:"queueId"`
		ClientRequestID string          `json:"clientRequestId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
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
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	runtimeCmds := map[string]struct{}{}
	parsed = command.ResolveCommand(parsed, snapshot, runtimeCmds)
	if parsed.Kind == command.KindUnknown && s.ext != nil {
		tgPre := toggles.Load(s.cfg.Home)
		s.ext.Prepare(r.Context(), id, sess.Header.CWD, extension.Enabled(snapshot.Extensions, tgPre.Extensions)) // chain order via Enabled
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
	spec := cmp.Or(body.Model, sess.Config.Model)
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
	s.startRun(context.WithoutCancel(r.Context()), w, id, body.Content, body.ParentID, body.Model, body.ClientRequestID)
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
	spec := cmp.Or(model, sess.Config.Model)
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
		// Why: occupy must be paired with release. Closing done here used to
		// mark SSE idle without consuming pendingReload, so a /reload during
		// the prompt never invalidated the snapshot. release closes done (and
		// Broadcasts) itself; a second close panics.
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
		// Use the credential snapshot held by this run rather than mutable
		// registry state; a concurrent login must not relabel opaque output.
		bindingCredential = liveCredential
	} else if credential, status, err := s.registry.Credential(info.Provider); err == nil && status.Configured {
		bindingCredential = credential
	}
	jobs := s.processesFor(id)
	profile := toolProfile(info)
	// The resource snapshot is loaded before the tool set because shell tools
	// carry the extension-contributed PATH directories, and it stays the single
	// source for this session's extension catalog below.
	snapshot := s.resources.Load(sess.ID(), sess.Header.CWD)
	s.reportManifestErrors(sess.ID(), snapshot.Extensions)
	// Why the prompt does not resolve the session's Agent depth: the tool set
	// feeds both the provider's tool schemas and the system prompt's tool list,
	// so making it depend on the durable parent chain meant a deep child (or a
	// session whose ancestry changed mid-conversation) rendered a different
	// prefix from its parent and lost the prefix cache. Depth is enforced at
	// spawn time instead; see builtin.Set.Build and Server.SpawnAgent.
	tls := builtin.Set{
		CWD: sess.Header.CWD, Processes: jobs, Agent: s,
		// Leave the entry unset so SpawnAgent resolves the leaf at the actual
		// Agent tool-call boundary, after the current user/assistant history has
		// been appended. Capturing it while assembling tools would fork stale
		// parent context.
		AgentParentSessionID: sess.ID(),
		Shells:               s.shells, Mutations: s.mutations,
		PathDirs: snapshot.PathDirs,
	}.Build(profile)
	tg := toggles.Load(cfg.Home)
	// Apply the global built-in toggle before extension tools are appended. This
	// keeps the built-in setting scoped to Set.Build and leaves extensions under
	// their own lifecycle/session controls.
	tls = builtin.FilterBuiltins(tls, tg.Tools)
	// snapshot.Extensions is the global Discover.All catalog. Configure
	// reconciles process-global sidecars; Prepare builds this session's view.
	enabledExtensions := extension.Enabled(snapshot.Extensions, tg.Extensions)
	s.ext.Configure(enabledExtensions)
	extTools := s.ext.Prepare(ctx, sess.ID(), sess.Header.CWD, enabledExtensions)
	tls = append(tls, extTools...)
	if ctx.Err() != nil {
		st.err = ctx.Err()
		return
	}
	tls = s.filterActiveTools(id, tls)
	occ := s.ext.Occupy(id)
	if s.requireModelCredential {
		if s.providerExtensions != nil && s.providerExtensions.HasProvider(liveModel.Provider) {
			runStreamer = s.providerExtensions.NewStreamer(liveModel, liveCredential)
		} else {
			runStreamer = provider.NewLiveModel(liveModel, liveKey, occ.HTTPDoer()).WithIdleTimeout(time.Duration(s.cfg.Streaming.IdleTimeoutSeconds) * time.Second)
			runStreamer = occ.WrapStreamer(runStreamer)
		}
	}
	// The snapshot is fixed for the session until reload, while the prompt is
	// rendered per request so tool schemas and runtime metadata stay current.
	promptInput := prompt.Input{Resources: snapshot, Tools: tls, Toggle: tg.Skills}
	sys := prompt.Build(promptInput)

	useServerCompaction := s.serverSideCompaction(info) && occ.HTTPDoer() == nil
	replayBinding := replayBindingForSession(sess, info, bindingCredential)
	requestBinding := replayBinding
	inlineBinding := providerBindingForProtocol(info, bindingCredential, info.Compaction.Inline)
	opaqueReplaySafe := occ.OpaqueReplaySafe()
	if !opaqueReplaySafe {
		// Lifecycle hooks only see portable messages. Replaying an encrypted
		// prefix would bypass a newly enabled DLP/moderation hook, and provider
		// hooks can redirect it outside the scope recorded in its binding.
		replayBinding = types.ProviderBinding{}
		useServerCompaction = false
	}
	emitter := &runEmitter{
		s: s, ctx: ctx, id: id, sess: sess, st: st, info: info,
		idempotencyKey: idempotencyKey, serverSide: useServerCompaction,
		binding: inlineBinding, replayBinding: replayBinding,
	}
	emit := emitter.Emit

	// Preflight before running: a resumed session or a very large prompt may already
	// exceed the context window, so compact once before loop.Run. This is non-blocking:
	// a compaction failure only emits a warning.
	if !useServerCompaction && s.shouldCompact(sess, info, replayBinding) {
		current := sess.ContextToLeaf(replayBinding)
		preflight := &loop.Request{
			SessionID: id, System: sys, Messages: current.Messages,
			Provider: info.Provider, Model: info.ID, API: info.API,
			ProviderBinding:         requestBinding,
			Tools:                   toolSpecs(tls),
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
			// A standalone preflight may replace an inline checkpoint with a
			// different protocol. Resolve the newly committed binding before
			// the first generation request instead of expanding portable
			// history through the stale preflight binding.
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
		Tools:                   tls,
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
	user := types.Message{Role: "user", Content: content, Origin: origin, External: externalMeta, ClientRequestID: st.inputMetadata.ClientRequestID, Completion: st.inputMetadata.Completion}
	if user.ClientRequestID == "" {
		user.ClientRequestID = st.runID + ":input"
	}
	err = runMessage(user)
	// loop.Run checks Inbox.Has at the end of a turn. A steer can arrive in
	// the narrow interval after that check and before Run returns; atomically
	// close the handoff window only after taking one last Inbox snapshot, then
	// run any such message as a continuation. If the window is already closed,
	// SendMessage falls back to AgentController's durable queue/resume path.
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
				// Why: an accepted steer is committed to the tree only when
				// loop.Run drains the Inbox. A canceled run (abort) never
				// drains, so the optimistic bubble would vanish on the next
				// jsonl reload. Commit the message here as an unanswered user
				// turn: the text survives, and the next run sees it in history.
				if _, aerr := s.commitUserMessage(steer, func() error {
					_, _, err := sess.AppendMessageWithKey(steer, "")
					return err
				}); aerr != nil {
					slog.Warn("persist undrained steer", "session_id", id, "err", aerr)
					// The deferred handoff retries through the durable queue;
					// failed persistence must not consume a completion.
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

// persistRunCancellation commits the user-visible cancellation row after the
// loop's terminal output. It is a normal non-message leaf: provider replay
// ignores it, while session history and branch navigation retain it in the
// exact position where the run stopped.
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

func (s *Server) summarizer(ctx context.Context, sessionID, prov, model string) compact.Summarizer {
	stream := s.liveSummarizer(ctx, sessionID, prov, model)
	return compact.StreamSummarizer{
		Stream: func(ctx context.Context, system, user string) (string, *types.Usage, error) {
			m, err := stream.Stream(ctx, loop.Request{
				Provider:  prov,
				Model:     model,
				SessionID: sessionID,
				System:    system,
				Messages:  []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: user}}}},
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

func (s *Server) remoteCompactThreshold(model provider.Model) int {
	window := model.ContextWindow
	if cap := s.cfg.Compaction.MaxContextTokens; cap > 0 && cap < window {
		window = cap
	}
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
	return compact.ShouldRun(compact.EstimateModelContext(modelContext, sess.LastCompactionAt()), model.ContextWindow, s.cfg.Compaction)
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
	// A standalone provider can compact a window that is too small for the
	// local cut policy. The hook still receives a portable plan, never the
	// opaque Responses prefix.
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
	// Header interceptors can replace the endpoint or project/account identity
	// after binding is computed. Until they expose a stable scope fingerprint,
	// encrypted context cannot safely cross that boundary.
	if s.ext != nil && s.ext.HTTPDoer(sess.ID()) != nil {
		return nil, fmt.Errorf("%w with provider header interception", errRemoteCompactionUnavailable)
	}
	compactor, binding := s.liveCompactor(ctx, sess.ID(), model)
	if compactor == nil {
		return nil, errRemoteCompactionUnavailable
	}
	if failed != nil && failed.ProviderBinding.Provider != "" &&
		!failed.ProviderBinding.SameScope(binding) {
		// Overflow recovery resumes through the already occupied streamer. A
		// rotated credential or protocol would make the new checkpoint unsafe
		// for that stream.
		return nil, errors.New("provider credential or compaction scope changed during remote compaction")
	}

	req := loop.Request{
		SessionID: sess.ID(), Provider: model.Provider, Model: model.ID, API: model.API,
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
	opaqueReplaySafe := s.ext == nil || s.ext.Occupy(sess.ID()).OpaqueReplaySafe()
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
	if !req.ContextTransformed && s.ext != nil {
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
	if !req.ContextTransformed && s.ext != nil {
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
	if s.ext != nil {
		transformed, transformErr := s.ext.Occupy(sess.ID()).TransformProviderRequest(ctx, req)
		if transformErr != nil {
			// A failed provider policy must not be bypassed by local fallback.
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
		// The usage carried by these messages was measured against an opaque
		// checkpoint that is incompatible with the selected model. The request
		// now contains expanded portable history, so only a full-history estimate
		// is valid.
		tokensBefore = compact.EstimateModelContext(types.ModelContext{Messages: req.Messages, Portable: true}, sess.LastCompactionAt())
	}
	return &remoteCompactionPlan{
		sourceLeafID: sess.LeafID(), request: req, binding: binding,
		compactor:      compactor,
		tokensBefore:   tokensBefore,
		contextChanged: contextChanged,
	}, nil
}

// compactSession is the shared orchestrator for manual, preflight, threshold,
// and overflow compaction. Planning, extension interception, strategy
// execution, validation, and commit are separate stages.
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
	if s.ext != nil {
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
			s.summarizer(ctx, sess.ID(), sess.Config.Provider, sess.Config.Model),
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
	// compactSession runs inside an occupied prompt, so this queues until
	// runPrompt's release instead of closing this turn's extension views.
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
	// Heartbeats and event writes share one writer; a failed heartbeat cancels
	// the reader's condition wait instead of leaving a dead connection parked.
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
	// Registering before the first read tells the emitter which buffered
	// payloads this reader is still owed, so nothing is trimmed out from under
	// it (see runState.trimLocked).
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
	}() // Prevent a client disconnect from leaking the goroutine.
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
		// Blank carries a payload the emitter dropped as superseded; since
		// marks events this client already has from an earlier connection.
		if ev.Blank || ev.Seq <= since || snapshot.covers(ev) {
			continue
		}
		if ev.Type == loop.AgentEnd {
			// A terminal frame is a client's cue to issue the next prompt or
			// compaction. Wait for release (including queued reload), otherwise
			// cache work in release turns that next request into a spurious 409.
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

// parseCursor reads the resume position the client sent, from the SSE
// Last-Event-ID header (EventSource resends it on its own) or a `since` query
// (fetch clients set it themselves). Both carry "<runID>:<seq>"; a cursor from
// another run is ignored so a stale value cannot skip the current run.
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
	if snapshot, ok := s.agentTasks.TaskForSession(id); ok && (snapshot.Status == agent.Running || snapshot.Status == agent.Pending) {
		_, err := s.agentTasks.Stop(snapshot.TaskID)
		abortedAgent = err == nil
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
	// Why: /compact is answered by one synchronous request, so the chat UI has
	// no run stream to watch. Publish progress on the session notification
	// stream so the history shows a live "compacting" row.
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
	// A manual /compact has no run stream, so its rebuilt context would not
	// reach the meter until the next prompt's request_header without this push.
	s.publishContextUsage(sess)
	writeJSON(w, 200, map[string]any{
		"id": outcome.Entry.ID, "type": outcome.Entry.Type,
		"firstKeptEntryId": outcome.FirstKeptEntryID, "handled": true,
		"strategy": outcome.Strategy,
	})
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
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
	// An explicit entryId copies a settled prefix, so it is safe to fork a live
	// session at a specific message (the WebUI only offers per-message forks).
	// A leaf fork (no entryId) would copy whatever the running turn has written
	// so far, so it stays rejected while the session is busy.
	if body.EntryID == "" && s.running(r.PathValue("id")) {
		http.Error(w, "session busy", http.StatusConflict)
		return
	}
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
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
