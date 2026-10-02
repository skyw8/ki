package process

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestProcessPublisherCoalescesBlockedListenerAndAlwaysPublishesExit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var updates []Update
	p := newProcessPublisher(func(update Update) {
		if update.Process.Revision == 1 {
			close(started)
			<-release
		}
		updates = append(updates, update)
	})
	go p.run(Snapshot{Revision: 1, Status: "running"})
	<-started
	for revision := uint64(2); revision < 100; revision++ {
		p.enqueue(Update{Process: Snapshot{Revision: revision, Status: "running"}})
	}
	p.enqueue(Update{Delta: "tail", Process: Snapshot{Revision: 100, Status: "exited"}})
	p.enqueue(Update{Process: Snapshot{Revision: 99, Status: "running"}})
	close(release)
	<-p.done
	if len(updates) != 2 || updates[0].Process.Revision != 1 || updates[1].Process.Status != "exited" || updates[1].Delta != "tail" {
		t.Fatalf("lifecycle/progress ordering: %+v", updates)
	}
}

func TestProcessPublisherProgressLimitDoesNotSuppressTerminalLifecycle(t *testing.T) {
	progress := make(chan Update, 4)
	p := newProcessPublisher(func(update Update) { progress <- update })
	p.remaining = 1
	go p.run(Snapshot{Revision: 1, Status: "running"})
	<-progress
	p.enqueue(Update{Process: Snapshot{Revision: 2, Status: "running"}})
	if got := <-progress; got.Process.Revision != 2 {
		t.Fatalf("first progress missing: %+v", got)
	}
	p.enqueue(Update{Process: Snapshot{Revision: 3, Status: "running"}})
	p.enqueue(Update{Process: Snapshot{Revision: 4, Status: "exited"}})
	<-p.done
	if got := <-progress; got.Process.Status != "exited" || len(progress) != 0 {
		t.Fatalf("budget dropped final or emitted excess progress: %+v", got)
	}
}

func TestManagerCloseWaitsForFinalPublicationAndSupportsReentrantObservation(t *testing.T) {
	shells := DiscoverShellRuntime()
	if !shells.BashAvailable() {
		t.Skip("Bash unavailable for POSIX command fixtures")
	}
	shell, err := ResolveShell(shells.WithoutPowerShell(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	final := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); manager.Close() })
	manager.SetListener(func(update Update) {
		if update.Process.Status != "exited" {
			return
		}
		// Final callbacks may inspect/terminate the process without waiting on
		// their own publication barrier.
		if _, err := manager.Terminate(update.Process.SessionID); err != nil {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := manager.Interact(ctx, update.Process.SessionID, "", time.Millisecond, 100, nil); err != nil {
			t.Error(err)
		}
		close(final)
		<-release
	})
	id, err := manager.Start(t.Context(), shell, t.TempDir(), "printf final", false, Identity{})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := manager.get(id)
	<-final
	closed := make(chan struct{})
	go func() { manager.Close(); close(closed) }()
	// The callback is observably blocked, so Close must not report completion.
	select {
	case <-closed:
		t.Fatal("Close abandoned a final listener")
	case <-time.After(25 * time.Millisecond):
		// This interval tests the blocking contract itself, not process startup.
	}
	once.Do(func() { close(release) })
	<-closed
	<-p.publisher.done
	if p.peek().Status != "exited" {
		t.Fatal("process writer not reaped")
	}
}

func TestBatchStopRequestsFanOutBeforeAnyHelperCompletes(t *testing.T) {
	ps := make([]*shellProcess, 8)
	for i := range ps {
		ps[i] = &shellProcess{stopped: make(chan struct{}), snapshot: Snapshot{Status: "exited"}}
		// Gate the platform helper's signaling lock without fixed sleeps or
		// spawning actual processes. Request fanout must never take this lock.
		ps[i].control.Lock()
	}
	stopped := requestStops(ps)
	for _, p := range ps {
		called := false
		p.terminate.Do(func() { called = true })
		if called {
			t.Fatal("batch waited before registering every stop request")
		}
		select {
		case <-p.stopped:
			t.Fatal("gated helper unexpectedly completed")
		default:
		}
		p.control.Unlock()
	}
	for _, done := range stopped {
		<-done
	}
}
