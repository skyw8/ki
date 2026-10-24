package loop

import (
	"context"
	"sync"
	"time"

	toolapi "ki/internal/tool"
)

type ToolScheduler struct {
	mu      sync.Mutex
	limit   int
	active  int
	groups  map[string]*toolGroup
	pending []*toolAdmission
}

type toolGroup struct {
	active    int
	exclusive bool
}

type toolAdmission struct {
	policy toolapi.ExecutionPolicy
	ready  chan struct{}
	active bool
}

func NewToolScheduler(limit int) *ToolScheduler {
	if limit <= 0 {
		limit = 32
	}
	return &ToolScheduler{limit: limit, groups: make(map[string]*toolGroup)}
}

func (s *ToolScheduler) acquire(ctx context.Context, policy toolapi.ExecutionPolicy) (func(), time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if policy.Orchestrator {
		return func() {}, 0, nil
	}
	started := time.Now()
	request := &toolAdmission{policy: policy, ready: make(chan struct{})}
	s.mu.Lock()
	s.pending = append(s.pending, request)
	s.dispatchLocked()
	s.mu.Unlock()
	select {
	case <-request.ready:
	case <-ctx.Done():
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		if request.active {
			s.releaseLocked(request)
		} else {
			for i, pending := range s.pending {
				if pending == request {
					s.pending = append(s.pending[:i], s.pending[i+1:]...)
					break
				}
			}
		}
		s.dispatchLocked()
		s.mu.Unlock()
		return nil, time.Since(started), err
	}
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.releaseLocked(request)
			s.dispatchLocked()
			s.mu.Unlock()
		})
	}, time.Since(started), nil
}

func (s *ToolScheduler) releaseLocked(request *toolAdmission) {
	if !request.active {
		return
	}
	request.active = false
	s.active--
	if group := s.groups[request.policy.Group]; group != nil {
		group.active--
		if request.policy.Exclusive {
			group.exclusive = false
		}
		if group.active == 0 {
			delete(s.groups, request.policy.Group)
		}
	}
}

func (s *ToolScheduler) dispatchLocked() {
	blocked := make(map[string]bool)
	for i := 0; i < len(s.pending) && s.active < s.limit; {
		request := s.pending[i]
		policy := request.policy
		group := s.groups[policy.Group]
		limit := policy.MaxConcurrent
		if limit <= 0 {
			limit = s.limit
		}
		if policy.Group != "" && (blocked[policy.Group] || group != nil && (group.exclusive || group.active >= limit || policy.Exclusive && group.active > 0)) {
			blocked[policy.Group] = true
			i++
			continue
		}
		s.pending = append(s.pending[:i], s.pending[i+1:]...)
		s.active++
		request.active = true
		if policy.Group != "" {
			if group == nil {
				group = &toolGroup{}
				s.groups[policy.Group] = group
			}
			group.active++
			group.exclusive = policy.Exclusive
		}
		close(request.ready)
	}
}
