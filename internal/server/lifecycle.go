package server

import (
	"sync"

	"ki/internal/session"
)

// Admission and writer accounting share runtimeMu. Keep per-session owners as
// well as the server-wide barrier: a new occupy can replace s.runs[id] after
// done while the old release callback is still writing.
func (s *Server) addSessionWriterLocked(id string) {
	group := s.sessionWG[id]
	if group == nil {
		group = &sync.WaitGroup{}
		s.sessionWG[id] = group
	}
	group.Add(1)
	s.runtimeWG.Add(1)
}

func (s *Server) finishSessionWriter(id string) {
	s.runtimeMu.Lock()
	group := s.sessionWG[id]
	s.runtimeMu.Unlock()
	group.Done()
	s.runtimeWG.Done()
}

// sessionAdmissionError is checked while an input gate is held before mutating
// a queue. Selection is session-scoped; deletion/shutdown own this lifecycle fence.
func (s *Server) sessionAdmissionError(id string) error {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeClosed {
		return errRuntimeClosed
	}
	if s.deleting[id] {
		return errSessionNotFound
	}
	if s.stopping[id] > 0 {
		return errSessionBusy
	}
	return nil
}

func (s *Server) blockSessionAdmissions(ids []string) func() {
	s.runtimeMu.Lock()
	for _, id := range ids {
		s.stopping[id]++
	}
	s.runtimeMu.Unlock()
	return sync.OnceFunc(func() {
		s.runtimeMu.Lock()
		for _, id := range ids {
			s.stopping[id]--
			if s.stopping[id] == 0 {
				delete(s.stopping, id)
			}
		}
		s.runtimeMu.Unlock()
		// Queue contents survive tree abort. Only resume after old processes and
		// callbacks are cleaned; a replacement run must not join that cleanup.
		for _, id := range ids {
			s.dispatchQueue(id)
		}
	})
}

func (s *Server) waitSessionWriters(ids []string) {
	s.runtimeMu.Lock()
	groups := make([]*sync.WaitGroup, 0, len(ids))
	for _, id := range ids {
		if group := s.sessionWG[id]; group != nil {
			groups = append(groups, group)
		}
	}
	s.runtimeMu.Unlock()
	for _, group := range groups {
		group.Wait()
	}
}

func (s *Server) beginDeletion() (func(), error) {
	// Serialize overlapping cascades, without serializing unrelated agent work.
	s.deletionMu.Lock()
	s.runtimeMu.Lock()
	if s.runtimeClosed {
		s.runtimeMu.Unlock()
		s.deletionMu.Unlock()
		return nil, errRuntimeClosed
	}
	s.runtimeWG.Add(1)
	s.runtimeMu.Unlock()
	return func() {
		s.deletionMu.Unlock()
		s.runtimeWG.Done()
	}, nil
}

func (s *Server) markDeleting(ids []string) []<-chan struct{} {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	var warmups []<-chan struct{}
	for _, id := range ids {
		s.deleting[id] = true
		if prep := s.runtime[id]; prep != nil && prep.inflight {
			if prep.cancel != nil {
				prep.cancel()
			}
			if prep.done != nil {
				warmups = append(warmups, prep.done)
			}
		}
	}
	return warmups
}

func (s *Server) fenceDeletionInputs(infos []session.Info) {
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	s.markDeleting(ids)
	s.mu.Lock()
	for _, id := range ids {
		if st := s.runs[id]; st != nil {
			s.cancelRun(id, st, cancelReasonSessionDelete, "server", false)
		}
	}
	s.mu.Unlock()
	// A preaccepted HTTP fork holds this gate through child materialization.
	// Drain it before the next traversal; that child must join the cascade.
	for _, id := range ids {
		gate := s.inputGate(id)
		gate.Lock()
		gate.Unlock()
	}
}

func (s *Server) deleteSessionInfos(infos []session.Info, reservations []<-chan struct{}) error {
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	warmups := s.markDeleting(ids)
	// Cancel every writer before waiting for any: a parent may be waiting for
	// a child, or spawning while a child is unwinding. No input can reoccupy.
	s.mu.Lock()
	runs := make([]*runState, 0, len(ids))
	for _, id := range ids {
		if st := s.runs[id]; st != nil {
			s.cancelRun(id, st, cancelReasonSessionDelete, "server", false)
			runs = append(runs, st)
		}
	}
	s.mu.Unlock()
	for _, st := range runs {
		if st.released != nil {
			<-st.released
		} else {
			<-st.done
		}
	}
	s.waitSessionWriters(ids)
	for _, info := range infos {
		if s.agentTasks != nil {
			if task, ok := s.agentTasks.TaskForSession(info.ID); ok {
				_, _ = s.agentTasks.Stop(task.TaskID)
			}
		}
	}
	for _, done := range reservations {
		<-done
	}
	for _, done := range warmups {
		<-done
	}
	// Callers supply descendants first. Input gates drain accepted queue writes
	// before removal; new messages see the tombstone instead of recreating files.
	for _, info := range infos {
		gate := s.inputGate(info.ID)
		gate.Lock()
		err := s.removeSessionInfo(info)
		gate.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// fenceAgentDeletion also waits for spawns reserved before the fence. Those
// reservations release only after SpawnAgent's rollback removes the child.
func (s *Server) fenceAgentDeletion(id string) (func(), <-chan struct{}, error) {
	if s.agentTasks != nil {
		return s.agentTasks.FenceSubtreeAdmission(id)
	}
	done := make(chan struct{})
	close(done)
	return func() {}, done, nil
}
