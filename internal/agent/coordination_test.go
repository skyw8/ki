package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"ki/internal/types"
)

func TestAgentCapacityIsPerRootAndQueuedFollowupWakesOnRelease(t *testing.T) {
	c := NewController()
	c.SetMaxConcurrent(1)
	defer c.Close()
	held := make(chan struct{})
	ready := make(chan struct{})
	followup := make(chan string, 1)
	quick, err := c.Start(t.Context(), Request{TaskName: "quick", TaskPath: "/root/quick", RootSessionID: "root", SessionID: "quick"}, "", func(_ context.Context, _, prompt string) (Completion, error) {
		if prompt != "" {
			followup <- prompt
		}
		return Completion{Result: prompt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Wait(t.Context(), quick.TaskID); err != nil {
		t.Fatal(err)
	}
	// Wait observes a finished result before controller callbacks have fully drained.
	c.tasks[quick.TaskID].mu.Lock()
	done := c.tasks[quick.TaskID].runDone
	c.tasks[quick.TaskID].mu.Unlock()
	if done != nil {
		<-done
	}
	blocker, err := c.Start(t.Context(), Request{TaskName: "held", TaskPath: "/root/held", RootSessionID: "root", SessionID: "held"}, "", func(ctx context.Context, _, _ string) (Completion, error) {
		close(ready)
		select {
		case <-held:
			return Completion{}, nil
		case <-ctx.Done():
			return Completion{}, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	if _, err := c.Start(t.Context(), Request{RootSessionID: "root"}, "", func(context.Context, string, string) (Completion, error) { return Completion{}, nil }); !errors.Is(err, errAgentCapacity) {
		t.Fatalf("same-root capacity: %v", err)
	}
	other, err := c.Start(t.Context(), Request{RootSessionID: "other"}, "", func(context.Context, string, string) (Completion, error) { return Completion{}, nil })
	if err != nil {
		t.Fatalf("separate root: %v", err)
	}
	_, _ = c.Wait(t.Context(), other.TaskID)
	if status, err := c.QueueOrResume(t.Context(), quick.TaskID, "next"); err != nil || status != "queued" {
		t.Fatalf("follow-up receipt: %s %v", status, err)
	}
	select {
	case <-followup:
		t.Fatal("capacity ignored")
	default:
	}
	close(held)
	_, _ = c.Wait(t.Context(), blocker.TaskID)
	select {
	case prompt := <-followup:
		if prompt != "next" {
			t.Fatal(prompt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accepted follow-up never woke on capacity release")
	}
	snapshot, err := c.Wait(t.Context(), quick.TaskID)
	if err != nil || snapshot.Generation != 2 || snapshot.Result != "next" {
		t.Fatalf("identity/generation: %+v %v", snapshot, err)
	}
}

func TestProgressIsGenerationScopedAndKeepsLifetimeStats(t *testing.T) {
	c := NewController()
	defer c.Close()
	started := make(chan string, 2)
	released := make(chan struct{}, 2)
	launch, err := c.Start(t.Context(), Request{Prompt: "first"}, "", func(ctx context.Context, id, prompt string) (Completion, error) {
		started <- id
		select {
		case <-released:
		case <-ctx.Done():
			return Completion{}, ctx.Err()
		}
		snap, _ := c.Get(id)
		return Completion{Result: snap.Result, ToolUseCount: snap.RunStats.Tools, TotalTokens: snap.RunStats.TotalTokens}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	emit := func(gen uint64, ev ProgressEvent) {
		t.Helper()
		ev.RunID = "run"
		if err := c.ReduceProgress(launch.TaskID, gen, ev); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		emit(1, ProgressEvent{Type: ToolStarted, CallID: id, ToolName: "read"})
		emit(1, ProgressEvent{Type: ToolFinished, CallID: id})
	}
	emit(1, ProgressEvent{Type: MessageCompleted, Text: "first result", Usage: &types.Usage{Input: 5, Output: 2, CacheRead: 10}})
	released <- struct{}{}
	first, err := c.Wait(t.Context(), launch.TaskID)
	if err != nil || first.RunStats.Tools != 3 || first.TotalTokens != 17 {
		t.Fatalf("first %+v %v", first, err)
	}
	if _, err := c.QueueOrResume(t.Context(), launch.TaskID, "second"); err != nil {
		t.Fatal(err)
	}
	<-started
	emit(2, ProgressEvent{Type: ToolStarted, CallID: "wait", ToolName: "wait_agent"})
	current, _ := c.Get(launch.TaskID)
	if current.Phase != "waiting_message" || current.WaitingFor != "mailbox" {
		t.Fatalf("wait progress %+v", current)
	}
	emit(2, ProgressEvent{Type: ToolFinished, CallID: "wait", DurationMs: 13})
	emit(2, ProgressEvent{Type: ToolStarted, CallID: "read", ToolName: "read"})
	emit(2, ProgressEvent{Type: ToolFinished, CallID: "read"})
	// A callback from the first generation cannot overwrite the current result.
	emit(1, ProgressEvent{Type: MessageCompleted, Text: "stale"})
	emit(2, ProgressEvent{Type: MessageCompleted, Usage: &types.Usage{Input: 4, Output: 1, TotalTokens: 5}})
	released <- struct{}{}
	second, err := c.Wait(t.Context(), launch.TaskID)
	if err != nil || second.Result != "" || second.RunStats.Tools != 2 || second.LifetimeStats.Tools != 5 || second.RunStats.TotalTokens != 5 || second.LifetimeStats.TotalTokens != 22 || second.RunStats.ContextTokens != 4 || second.RunStats.MailboxWaitMs != 13 {
		t.Fatalf("second %+v %v", second, err)
	}
}

func TestSubtreeCleanupFencesAlreadyReservedSpawnAndFollowup(t *testing.T) {
	controller := NewController()
	defer controller.Close()
	run := func(context.Context, string, string) (Completion, error) { return Completion{}, nil }
	child, err := controller.Start(t.Context(), Request{TaskName: "child", TaskPath: "/root/child", RootSessionID: "root", SessionID: "child", ParentSessionID: "root"}, "", run)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = controller.Wait(t.Context(), child.TaskID)
	reserved, err := controller.ReservePath("root", "/root/child/grandchild")
	if err != nil {
		t.Fatal(err)
	}
	defer reserved()
	release, err := controller.BlockSubtreeAdmission("child")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := controller.Start(t.Context(), Request{TaskPath: "/root/child/grandchild", RootSessionID: "root", SessionID: "grandchild"}, "", run); err == nil {
		t.Fatal("pre-existing reservation bypassed subtree fence")
	}
	if _, err := controller.ReservePath("root", "/root/child/another"); err == nil {
		t.Fatal("reserved during cleanup")
	}
	if _, err := controller.QueueOrResume(t.Context(), child.TaskID, "late task"); err == nil {
		t.Fatal("follow-up admitted during cleanup")
	}
	snapshot, _ := controller.Get(child.TaskID)
	if snapshot.PendingTasks != 0 {
		t.Fatal("rejected follow-up was journaled")
	}
	for _, req := range []Request{{TaskPath: "/root/sibling", RootSessionID: "root", SessionID: "sibling"}, {TaskPath: "/root/child/grandchild", RootSessionID: "other", SessionID: "other"}} {
		launch, err := controller.Start(t.Context(), req, "", run)
		if err != nil {
			t.Fatalf("unrelated task blocked: %v", err)
		}
		_, _ = controller.Wait(t.Context(), launch.TaskID)
	}
	release()
	release()
	if status, err := controller.QueueOrResume(t.Context(), child.TaskID, "new task"); err != nil || status != "resumed" {
		t.Fatalf("identity unusable after cleanup: %s %v", status, err)
	}
	_, _ = controller.Wait(t.Context(), child.TaskID)
}

func TestSubtreeFenceDrainsOnlyMatchingSpawnReservations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		controller := NewController()
		defer controller.Close()
		run := func(context.Context, string, string) (Completion, error) { return Completion{}, nil }
		child, err := controller.Start(t.Context(), Request{TaskPath: "/root/child", RootSessionID: "root", SessionID: "child", ParentSessionID: "root"}, "", run)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Wait(t.Context(), child.TaskID); err != nil {
			t.Fatal(err)
		}
		var releaseReservations []func()
		for _, test := range []struct{ root, path string }{
			{"root", "/root/child/grandchild"},
			{"root", "/root/childish"},
			{"other", "/root/child/grandchild"},
		} {
			release, err := controller.ReservePath(test.root, test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			releaseReservations = append(releaseReservations, release)
		}
		releaseFence, drained, err := controller.FenceSubtreeAdmission("child")
		if err != nil {
			t.Fatal(err)
		}
		defer releaseFence()
		if _, err := controller.Start(t.Context(), Request{TaskPath: "/root/child/grandchild", RootSessionID: "root"}, "", run); err == nil {
			t.Fatal("already-reserved spawn bypassed subtree fence")
		}
		synctest.Wait()
		select {
		case <-drained:
			t.Fatal("subtree fence did not retain an in-flight matching reservation")
		default:
		}
		releaseReservations[0]()
		releaseReservations[0]()
		synctest.Wait()
		select {
		case <-drained:
		default:
			t.Fatal("sibling or other-root reservation delayed child cleanup")
		}
		releaseRoot, rootDrained, err := controller.FenceSubtreeAdmission("root")
		if err != nil {
			t.Fatal(err)
		}
		defer releaseRoot()
		synctest.Wait()
		select {
		case <-rootDrained:
			t.Fatal("root fence omitted its sibling reservation")
		default:
		}
		releaseReservations[1]()
		synctest.Wait()
		select {
		case <-rootDrained:
		default:
			t.Fatal("other root delayed root cleanup")
		}
	})
}

func TestCapacityBlockedFollowupRemainsInterruptibleWithoutConsumingPriorResult(t *testing.T) {
	controller := NewController()
	controller.SetMaxConcurrent(2)
	defer controller.Close()
	firstRelease := make(chan struct{})
	blockerStarted := make(chan Launch, 1)
	blockerErr := make(chan error, 1)
	followupStarted := make(chan struct{}, 1)
	controller.SetListener(func(snapshot Snapshot) {
		if snapshot.SessionID != "child" || snapshot.Status != Completed || snapshot.PendingTasks != 1 {
			return
		}
		// Deterministically let another identity take the released slot before
		// this completed generation can admit its already-accepted follow-up.
		blocker, err := controller.Start(t.Context(), Request{RootSessionID: "root"}, "", func(ctx context.Context, _, _ string) (Completion, error) {
			<-ctx.Done()
			return Completion{}, ctx.Err()
		})
		controller.SetMaxConcurrent(1)
		blockerStarted <- blocker
		blockerErr <- err
	})
	launch, err := controller.Start(t.Context(), Request{RootSessionID: "root", SessionID: "child", Prompt: "first"}, "", func(ctx context.Context, _, prompt string) (Completion, error) {
		if prompt == "first" {
			select {
			case <-firstRelease:
			case <-ctx.Done():
				return Completion{}, ctx.Err()
			}
		} else {
			followupStarted <- struct{}{}
		}
		return Completion{Result: prompt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := controller.QueueOrResume(t.Context(), launch.TaskID, "follow-up"); err != nil || status != "queued" {
		t.Fatalf("queue: %s %v", status, err)
	}
	close(firstRelease)
	blocker := <-blockerStarted
	if err := <-blockerErr; err != nil {
		t.Fatal(err)
	}
	task, _ := controller.task(launch.TaskID)
	task.mu.Lock()
	runDone := task.runDone
	task.mu.Unlock()
	if runDone != nil {
		<-runDone
	}
	waiting, _ := controller.Get(launch.TaskID)
	if waiting.Status != Pending || waiting.Phase != "waiting_resource" || waiting.PendingTasks != 1 {
		t.Errorf("accepted follow-up appears terminal instead of waiting: %+v", waiting)
	}
	previous, err := controller.Wait(t.Context(), launch.TaskID)
	if err != nil || previous.Status != Completed || previous.Result != "first" {
		t.Fatalf("previous generation result changed while admission waited: %+v %v", previous, err)
	}
	stopped, err := controller.Stop(launch.TaskID)
	if err != nil || stopped.Status != Killed || stopped.PendingTasks != 0 {
		t.Errorf("pending interruption: %+v %v", stopped, err)
	}
	if currentCompletionDelivered(controller, launch.TaskID) {
		t.Error("interrupting pending work consumed the previous completed result")
	}
	if !commitSnapshotNotification(controller, previous) {
		t.Error("completed generation lost its parent notification")
	}
	if _, err := controller.Stop(blocker.TaskID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-followupStarted:
		t.Error("interrupted follow-up ran after capacity became available")
	default:
	}
}

func TestStopPendingWorkPreservesPriorGenerationCompletion(t *testing.T) {
	for _, status := range []Status{Pending, Completed, Failed, Interrupted} {
		t.Run(string(status), func(t *testing.T) {
			controller := NewController()
			defer controller.Close()
			previous := Snapshot{TaskID: "child", Generation: 1, Status: Completed, Result: "prior result"}
			done := make(chan struct{})
			close(done)
			controller.tasks["child"] = &agentTask{
				snap:     Snapshot{TaskID: "child", Generation: 1, Status: status, Progress: Progress{PendingTasks: 1}},
				pending:  []Input{{Prompt: "follow-up", ClientRequestID: "next"}},
				runCount: 1, done: done, doneClosed: true, result: &previous,
			}
			snapshot, err := controller.Stop("child")
			if err != nil || snapshot.Status != Killed || snapshot.PendingTasks != 0 {
				t.Errorf("interrupt pending work with prior %s: %+v %v", status, snapshot, err)
			}
			if controller.CompletionDelivered(types.CompletionIdentity{TaskID: "child", Generation: 1}) {
				t.Error("pending-only interruption consumed a previously completed generation")
			}
			if got, err := controller.Wait(t.Context(), "child"); err != nil || got.Status != Completed || got.Result != "prior result" {
				t.Fatalf("previous settled generation changed: %+v %v", got, err)
			}
		})
	}
}
