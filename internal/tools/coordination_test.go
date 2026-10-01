package tools

import (
	"context"
	"errors"
	"ki/internal/loop"
	"ki/internal/types"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAgentCapacityIsPerRootAndQueuedFollowupWakesOnRelease(t *testing.T) {
	c := NewAgentController()
	c.SetMaxConcurrent(1)
	defer c.Close()
	held := make(chan struct{})
	ready := make(chan struct{})
	followup := make(chan string, 1)
	quick, err := c.Start(t.Context(), AgentRequest{TaskName: "quick", TaskPath: "/root/quick", RootSessionID: "root", SessionID: "quick"}, "", func(_ context.Context, _, prompt string) (AgentCompletion, error) {
		if prompt != "" {
			followup <- prompt
		}
		return AgentCompletion{Result: prompt}, nil
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
	blocker, err := c.Start(t.Context(), AgentRequest{TaskName: "held", TaskPath: "/root/held", RootSessionID: "root", SessionID: "held"}, "", func(ctx context.Context, _, _ string) (AgentCompletion, error) {
		close(ready)
		select {
		case <-held:
			return AgentCompletion{}, nil
		case <-ctx.Done():
			return AgentCompletion{}, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	if _, err := c.Start(t.Context(), AgentRequest{RootSessionID: "root"}, "", func(context.Context, string, string) (AgentCompletion, error) { return AgentCompletion{}, nil }); !errors.Is(err, errAgentCapacity) {
		t.Fatalf("same-root capacity: %v", err)
	}
	other, err := c.Start(t.Context(), AgentRequest{RootSessionID: "other"}, "", func(context.Context, string, string) (AgentCompletion, error) { return AgentCompletion{}, nil })
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

func TestProcessOutputSplitUTF8IsIncrementalAndRawSpoolIsLossless(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	p := &shellProcess{file: file, changed: make(chan struct{}), snapshot: ProcessSnapshot{Status: "running"}}
	data := []byte("🙂中")
	if _, err = p.Write(data[:2]); err != nil {
		t.Fatal(err)
	}
	if got := p.observe(100).Output; got != "" {
		t.Fatalf("partial rune consumed: %q", got)
	}
	if _, err = p.Write(data[2:5]); err != nil {
		t.Fatal(err)
	}
	first := p.observe(100).Output
	if first != "🙂" || !utf8.ValidString(first) {
		t.Fatalf("first output %q", first)
	}
	if _, err = p.Write(data[5:]); err != nil {
		t.Fatal(err)
	}
	if got := p.observe(100).Output; got != "中" {
		t.Fatalf("continuation %q", got)
	}
	if got := p.observe(100).Output; got != "" {
		t.Fatalf("output replayed: %q", got)
	}
	raw, err := os.ReadFile(file.Name())
	if err != nil || string(raw) != string(data) {
		t.Fatalf("raw %q %v", raw, err)
	}
}

func TestProgressIsGenerationScopedAndKeepsLifetimeStats(t *testing.T) {
	c := NewAgentController()
	defer c.Close()
	started := make(chan string, 2)
	released := make(chan struct{}, 2)
	launch, err := c.Start(t.Context(), AgentRequest{Prompt: "first"}, "", func(ctx context.Context, id, prompt string) (AgentCompletion, error) {
		started <- id
		select {
		case <-released:
		case <-ctx.Done():
			return AgentCompletion{}, ctx.Err()
		}
		snap, _ := c.Get(id)
		return AgentCompletion{Result: snap.Result, ToolUseCount: snap.RunStats.Tools, TotalTokens: snap.RunStats.TotalTokens}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	emit := func(gen uint64, ev loop.Event) {
		t.Helper()
		ev.RunID = "run"
		if err := c.ReduceEvent(launch.TaskID, gen, ev); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		emit(1, loop.Event{Type: loop.ToolExecutionStart, ToolCallID: id, ToolName: "read"})
		emit(1, loop.Event{Type: loop.ToolExecutionEnd, ToolCallID: id})
	}
	emit(1, loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "first result"}}, Usage: &types.Usage{Input: 5, Output: 2, CacheRead: 10}}})
	released <- struct{}{}
	first, err := c.Wait(t.Context(), launch.TaskID)
	if err != nil || first.RunStats.Tools != 3 || first.TotalTokens != 17 {
		t.Fatalf("first %+v %v", first, err)
	}
	if _, err := c.QueueOrResume(t.Context(), launch.TaskID, "second"); err != nil {
		t.Fatal(err)
	}
	<-started
	emit(2, loop.Event{Type: loop.ToolExecutionStart, ToolCallID: "wait", ToolName: "wait_agent"})
	current, _ := c.Get(launch.TaskID)
	if current.Phase != "waiting_message" || current.WaitingFor != "mailbox" {
		t.Fatalf("wait progress %+v", current)
	}
	emit(2, loop.Event{Type: loop.ToolExecutionEnd, ToolCallID: "wait", DurationMs: 13})
	emit(2, loop.Event{Type: loop.ToolExecutionStart, ToolCallID: "read", ToolName: "read"})
	emit(2, loop.Event{Type: loop.ToolExecutionEnd, ToolCallID: "read"})
	// A callback from the first generation cannot overwrite the current result.
	emit(1, loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "stale"}}}})
	emit(2, loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: "assistant", Usage: &types.Usage{Input: 4, Output: 1, TotalTokens: 5}}})
	released <- struct{}{}
	second, err := c.Wait(t.Context(), launch.TaskID)
	if err != nil || second.Result != "" || second.RunStats.Tools != 2 || second.LifetimeStats.Tools != 5 || second.RunStats.TotalTokens != 5 || second.LifetimeStats.TotalTokens != 22 || second.RunStats.ContextTokens != 4 || second.RunStats.MailboxWaitMs != 13 {
		t.Fatalf("second %+v %v", second, err)
	}
}

func TestBlockedTTYInputObserverCanCancelWithoutKillingProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stty fixture is Unix-only; ConPTY input has separate platform coverage")
	}
	tool := testExec(t, nil)
	snapshot := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "stty -icanon -echo; sleep 120", "tty": true, "yield_time_ms": 250}))
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	value, err := tool.processes.Interact(ctx, snapshot.SessionID, strings.Repeat("x", 1024*1024), time.Second, 100, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || value.Status != "running" {
		t.Fatalf("blocked input did not release observer: %+v %v (%s)", value, err, time.Since(started))
	}
	if _, err := tool.processes.Terminate(snapshot.SessionID); err != nil {
		t.Fatal(err)
	}
	// Termination unblocks the manager-owned write and releases its input lock.
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	if value, err := tool.processes.Interact(ctx2, snapshot.SessionID, "", time.Millisecond, 100, nil); err != nil || value.Status != "exited" {
		t.Fatalf("input ownership leaked: %+v %v", value, err)
	}
}

func TestPTYSpoolFailureIsReportedEvenWhenCommandExitsSuccessfully(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "closed-spool"))
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	p := &shellProcess{file: file, done: make(chan struct{}), changed: make(chan struct{}), snapshot: ProcessSnapshot{Status: "running"}}
	if _, err := p.Write([]byte("lost output")); err == nil {
		t.Fatal("closed spool accepted output")
	}
	p.finish(0, nil)
	result := processResult(p.observe(100), nil)
	if !result.IsError || result.Diagnostic.FaultDomain != "harness" || result.Diagnostic.Kind != "process_io" {
		t.Fatalf("output failure hidden by zero exit: %+v", result)
	}
}

func TestSubtreeCleanupFencesAlreadyReservedSpawnAndFollowup(t *testing.T) {
	controller := NewAgentController()
	defer controller.Close()
	run := func(context.Context, string, string) (AgentCompletion, error) { return AgentCompletion{}, nil }
	child, err := controller.Start(t.Context(), AgentRequest{TaskName: "child", TaskPath: "/root/child", RootSessionID: "root", SessionID: "child", ParentSessionID: "root"}, "", run)
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
	if _, err := controller.Start(t.Context(), AgentRequest{TaskPath: "/root/child/grandchild", RootSessionID: "root", SessionID: "grandchild"}, "", run); err == nil {
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
	for _, req := range []AgentRequest{{TaskPath: "/root/sibling", RootSessionID: "root", SessionID: "sibling"}, {TaskPath: "/root/child/grandchild", RootSessionID: "other", SessionID: "other"}} {
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
