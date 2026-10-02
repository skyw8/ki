package server

import (
	"context"
	"time"

	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/toggles"
)

// warmupTimeout bounds one open-session extension view preparation.
const warmupTimeout = 25 * time.Second

type runtimePrep struct {
	ready    bool
	inflight bool
	done     chan struct{}
	cancel   context.CancelFunc
}

func (s *Server) runtimeReady(id string) bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	st := s.runtime[id]
	return st != nil && st.ready
}

func (s *Server) resetRuntime(id string) {
	s.runtimeMu.Lock()
	delete(s.runtime, id)
	s.runtimeMu.Unlock()
}

func (s *Server) resetRuntimeExcept(active map[string]bool) {
	s.runtimeMu.Lock()
	for id := range s.runtime {
		if !active[id] {
			delete(s.runtime, id)
		}
	}
	s.runtimeMu.Unlock()
}

// kickWarmup starts the session extension view preparation in the background.
// Why: the warmup boundary is opening this session (create/GET/fork), not
// List. The sidecars themselves are started at server boot; this only sends
// session.open and builds the session-scoped registration view.
func (s *Server) kickWarmup(id, cwd string) {
	if id == "" {
		return
	}
	s.runtimeMu.Lock()
	if s.runtimeClosed || s.deleting[id] || s.stopping[id] > 0 {
		s.runtimeMu.Unlock()
		return
	}
	st := s.runtime[id]
	if st == nil {
		st = &runtimePrep{}
		s.runtime[id] = st
	}
	if st.ready || st.inflight {
		s.runtimeMu.Unlock()
		return
	}
	st.inflight = true
	st.done = make(chan struct{})
	ctx, cancel := context.WithTimeout(s.runtimeCtx, warmupTimeout)
	st.cancel = cancel
	s.addSessionWriterLocked(id)
	s.runtimeMu.Unlock()
	go func() {
		defer s.finishSessionWriter(id)
		defer close(st.done)
		defer cancel()
		s.warmupSession(ctx, id, cwd, st)
	}()
}

func (s *Server) warmupSession(ctx context.Context, id, cwd string, st *runtimePrep) {
	defer s.finishRuntime(id, st)
	if cwd == "" {
		sess, err := s.open(id)
		if err != nil {
			return
		}
		cwd = sess.Header.CWD
		_ = sess.Close()
	}
	snapshot := s.resources.Load(id, cwd)
	s.reportManifestErrors(id, snapshot.Extensions)
	if s.ext != nil {
		tg := toggles.Load(s.cfg.Home)
		_ = s.ext.Prepare(ctx, id, cwd, extension.Enabled(snapshot.Extensions, tg.Extensions))
	}
}

func (s *Server) finishRuntime(id string, st *runtimePrep) {
	s.runtimeMu.Lock()
	still := s.runtime[id] == st
	if still {
		st.ready = true
		st.inflight = false
	}
	s.runtimeMu.Unlock()
	if !still {
		return
	}
	// Why: runtime_ready is a session notification, not an occupy event.
	// Putting it on runState.evs prepends it to prompt SSE replay.
	s.publishPush(id, loop.Event{Type: loop.RuntimeReady, OK: true})
}
