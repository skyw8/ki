package server

import (
	"container/list"
	"time"

	"ki/internal/loop"
	"ki/internal/memory"
	"ki/internal/types"
)

const (
	completedReplayBytes = 16 << 20
	completedReplayTTL   = 2 * time.Minute
	maxCompletedReplays  = 256
)

type completedReplay struct {
	id      string
	state   *runState
	bytes   int64
	expires time.Time
}

type replayCache struct {
	order  list.List
	items  map[string]*list.Element
	bytes  int64
	limit  int64
	ttl    time.Duration
	timer  *time.Timer
	closed bool
}

func newReplayCache() replayCache {
	return replayCache{items: map[string]*list.Element{}, limit: completedReplayBytes, ttl: completedReplayTTL}
}

func (s *Server) retainReplay(id string, st *runState) {
	st.mu.Lock()
	bytes := memory.Weight(struct {
		Events   []*loop.Event
		Input    types.Message
		External map[string]string
		Inbox    *loop.Inbox
		Pending  []int
		Keys     []string
	}{st.evs, st.inputMetadata, st.external, st.inbox, st.pending, []string{st.runID, st.promptKey, st.cancelReason, st.cancelSource}}) + 512
	st.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replay.closed || s.runs[id] != st {
		return
	}
	s.forgetReplayLocked(id)
	now := time.Now()
	record := completedReplay{id: id, state: st, bytes: bytes, expires: now.Add(s.replay.ttl)}
	s.replay.items[id] = s.replay.order.PushBack(record)
	s.replay.bytes += bytes
	s.pruneReplaysLocked(now)
	s.scheduleReplayExpiryLocked()
}

func (s *Server) forgetReplayLocked(id string) {
	if e := s.replay.items[id]; e != nil {
		record := e.Value.(completedReplay)
		s.replay.bytes -= record.bytes
		delete(s.replay.items, id)
		s.replay.order.Remove(e)
	}
}

func (s *Server) pruneReplaysLocked(now time.Time) {
	for e := s.replay.order.Front(); e != nil; e = s.replay.order.Front() {
		record := e.Value.(completedReplay)
		if record.expires.After(now) && s.replay.bytes <= s.replay.limit && len(s.replay.items) <= maxCompletedReplays {
			break
		}
		if s.runs[record.id] == record.state {
			delete(s.runs, record.id)
		}
		s.forgetReplayLocked(record.id)
	}
}

func (s *Server) scheduleReplayExpiryLocked() {
	if s.replay.timer != nil {
		s.replay.timer.Stop()
		s.replay.timer = nil
	}
	if s.replay.closed || s.replay.order.Len() == 0 {
		return
	}
	expires := s.replay.order.Front().Value.(completedReplay).expires
	s.replay.timer = time.AfterFunc(max(0, time.Until(expires)), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.replay.closed {
			return
		}
		s.pruneReplaysLocked(time.Now())
		s.scheduleReplayExpiryLocked()
	})
}
