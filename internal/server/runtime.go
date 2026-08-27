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
	if s.runtimeClosed {
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
	s.runtimeWG.Add(1)
	s.runtimeMu.Unlock()
	go func() {
		defer s.runtimeWG.Done()
		s.warmupSession(id, cwd, st)
	}()
}

func (s *Server) warmupSession(id, cwd string, st *runtimePrep) {
	defer s.finishRuntime(id, st)
	ctx, cancel := context.WithTimeout(s.runtimeCtx, warmupTimeout)
	defer cancel()
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
	ev := loop.Event{Type: loop.RuntimeReady, OK: true}
	s.mu.Lock()
	for subscriber := range s.eventSubscribers[id] {
		select {
		case subscriber <- ev:
		default:
		}
	}
	s.mu.Unlock()
}

func (s *Server) rewarmWatchers(active map[string]bool) {
	s.mu.Lock()
	var ids []string
	for id := range s.eventSubscribers {
		if !active[id] {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		sess, err := s.open(id)
		if err != nil {
			continue
		}
		cwd := sess.Header.CWD
		_ = sess.Close()
		s.kickWarmup(id, cwd)
	}
}
