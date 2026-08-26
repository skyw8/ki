package server

import (
	"context"
	"sync"
	"time"

	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/mcp"
	"ki/internal/toggles"
)

// warmupTimeout bounds one open-session resource preparation. Global
// extensions and session MCP connections are prepared in parallel.
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

// kickWarmup starts the global extension Prepare and session MCP Prepare in the background.
// Why: the warmup boundary is opening this session (create/GET/fork), not
// List and not serve boot. A sidebar of dozens of jsonl files must not spawn
// sidecars. GET does not await handshake so a slow npm/uvx install cannot
// block transcript. Prompt ensure is then a no-op.
func (s *Server) kickWarmup(id, cwd string) {
	if id == "" {
		return
	}
	s.runtimeMu.Lock()
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
	s.runtimeMu.Unlock()
	go s.warmupSession(id, cwd, st)
}

func (s *Server) warmupSession(id, cwd string, st *runtimePrep) {
	defer s.finishRuntime(id, st)
	ctx, cancel := context.WithTimeout(context.Background(), warmupTimeout)
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
	tg := toggles.Load(s.cfg.Home)
	mcpFile := snapshot.MCP
	mcpCached := snapshot.MCPServers
	rev := snapshot.Revision
	enabled := extension.Enabled(snapshot.Extensions, tg.Extensions)
	var wg sync.WaitGroup
	wg.Go(func() {
		if s.ext != nil {
			s.ext.Configure(snapshot.Extensions)
			s.ext.Prepare(ctx, id, cwd, enabled)
		}
	})
	wg.Go(func() {
		if s.mcp == nil {
			return
		}
		prepared := s.mcp.Prepare(ctx, id, mcpFile, tg.MCP, mcpCached)
		_, _ = s.resources.UpdateMCP(id, rev, prepared.States)
		for name, state := range prepared.States {
			if state.Status == mcp.StatusFailed {
				s.publishMCPEvent(id, mcp.Notification{Kind: "server_failed", Server: name, Message: state.Error})
			}
		}
	})
	wg.Wait()
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
	for subscriber := range s.mcpSubscribers[id] {
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
	for id := range s.mcpSubscribers {
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
