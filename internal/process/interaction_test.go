package process

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

func interactionFixture() (*Manager, *shellProcess) {
	p := &shellProcess{
		interaction: make(chan struct{}, 1), changed: make(chan struct{}),
		snapshot: Snapshot{SessionID: 1, Status: "running"},
	}
	p.appendOutputLocked([]byte("abcdefgh"))
	return &Manager{processes: map[int64]*shellProcess{1: p}}, p
}

func TestCanceledQueuedInteractionKeepsHandleAndDoesNotDrainOutput(t *testing.T) {
	manager, p := interactionFixture()
	p.interaction <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	ctxResult := make(chan error, 1)
	go func() {
		value, err := manager.Interact(ctx, 1, "", time.Hour, 1, nil)
		if value.SessionID != 1 || value.Output != "" {
			ctxResult <- errors.New("queued cancellation lost handle or consumed output")
			return
		}
		ctxResult <- err
	}()
	cancel()
	if err := <-ctxResult; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-p.interaction
	if got := p.observe(1).Output; got != "abcd" {
		t.Fatalf("queued observer advanced cursor: %q", got)
	}
}

func TestPreCanceledInteractionNeverWritesOrConsumesOutput(t *testing.T) {
	manager, p := interactionFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 100 {
		value, err := manager.Interact(ctx, 1, "not PTY input", time.Hour, 1, nil)
		if !errors.Is(err, context.Canceled) || value.SessionID != 1 || value.Output != "" {
			t.Fatalf("pre-canceled interaction executed: %+v %v", value, err)
		}
	}
	if got := p.observe(1).Output; got != "abcd" {
		t.Fatalf("canceled observer advanced cursor: %q", got)
	}
}

func TestConcurrentInteractionsSerializeOutputConsumption(t *testing.T) {
	manager, p := interactionFixture()
	p.snapshot.Status = "exited"
	results := make(chan Snapshot, 2)
	for range 2 {
		go func() {
			value, err := manager.Interact(t.Context(), 1, "", time.Hour, 1, nil)
			if err != nil {
				t.Error(err)
			}
			results <- value
		}()
	}
	values := []Snapshot{<-results, <-results}
	sort.Slice(values, func(i, j int) bool { return values[i].OutputOffset < values[j].OutputOffset })
	if values[0].Output != "abcd" || values[0].OutputOffset != 0 || values[1].Output != "efgh" || values[1].OutputOffset != 4 {
		t.Fatalf("overlapping output consumption: %+v", values)
	}
}
