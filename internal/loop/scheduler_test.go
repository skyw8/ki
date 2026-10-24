package loop

import (
	"context"
	"sync"
	"testing"

	toolapi "ki/internal/tool"
)

func TestToolSchedulerGroupOrderingCancellationAndIsolation(t *testing.T) {
	s := NewToolScheduler(4)
	ctx := context.Background()
	read := toolapi.ExecutionPolicy{Group: "server", MaxConcurrent: 2}
	release1, _, err := s.acquire(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	release2, _, err := s.acquire(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	blocked := make(chan error, 1)
	go func() {
		release, _, err := s.acquire(cancelCtx, toolapi.ExecutionPolicy{Group: "server", Exclusive: true})
		if release != nil {
			release()
		}
		blocked <- err
	}()
	other, _, err := s.acquire(ctx, toolapi.ExecutionPolicy{Group: "other", Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	other()
	cancel()
	if err := <-blocked; err != context.Canceled {
		t.Fatalf("cancel error = %v", err)
	}
	release1()
	release2()
	release2()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != 0 || len(s.pending) != 0 || len(s.groups) != 0 {
		t.Fatalf("scheduler leaked: active=%d pending=%d groups=%v", s.active, len(s.pending), s.groups)
	}
}

func TestToolSchedulerOrchestratorDoesNotHoldPermit(t *testing.T) {
	s := NewToolScheduler(1)
	outer, _, err := s.acquire(context.Background(), toolapi.ExecutionPolicy{Orchestrator: true})
	if err != nil {
		t.Fatal(err)
	}
	defer outer()
	inner, _, err := s.acquire(context.Background(), toolapi.ExecutionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	inner()
}

func TestToolSchedulerBoundsConcurrentCalls(t *testing.T) {
	s := NewToolScheduler(3)
	var mu sync.Mutex
	active, peak := 0, 0
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			release, _, err := s.acquire(context.Background(), toolapi.ExecutionPolicy{Group: "shared", MaxConcurrent: 2})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			active++
			peak = max(peak, active)
			mu.Unlock()
			mu.Lock()
			active--
			mu.Unlock()
			release()
		})
	}
	wg.Wait()
	if peak > 2 || peak == 0 {
		t.Fatalf("peak = %d", peak)
	}
}
