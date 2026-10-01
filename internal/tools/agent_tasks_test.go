package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/state"
	"ki/internal/types"
)

func TestAgentControllerBackgroundStop(t *testing.T) {
	store := NewAgentController()
	started := make(chan struct{})
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "long child", Prompt: "wait", SessionID: "child-session",
	}, filepath.Join(t.TempDir(), "child-events.jsonl"), func(ctx context.Context, _, _ string) (AgentCompletion, error) {
		close(started)
		<-ctx.Done()
		return AgentCompletion{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := store.Stop(launch.TaskID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Wait(context.Background(), launch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != TaskKilled || snapshot.SessionID != "child-session" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestAgentControllerDurableDeliverySuppressesDuplicateCompletion(t *testing.T) {
	metadata := filepath.Join(t.TempDir(), "agent.json")
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "child", Prompt: "first", SessionID: "child-session", MetadataPath: metadata,
	}, "child.jsonl", func(_ context.Context, _, prompt string) (AgentCompletion, error) {
		return AgentCompletion{Result: prompt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	// A durable parent receipt suppresses duplicate notification of that run.
	commitCurrentNotification(store, launch.TaskID)
	if !currentCompletionDelivered(store, launch.TaskID) {
		t.Fatal("read run was not marked consumed")
	}
	if commitCurrentNotification(store, launch.TaskID) {
		t.Fatal("a consumed run still claimed a completion notification")
	}
	// A resume is a new run with a new result, so it notifies again.
	if status, err := store.QueueOrResume(context.Background(), launch.TaskID, "second"); err != nil || status != "resumed" {
		t.Fatalf("resume = %q %v", status, err)
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	if currentCompletionDelivered(store, launch.TaskID) {
		t.Fatal("resumed run inherited the consumed mark")
	}
	if !commitCurrentNotification(store, launch.TaskID) {
		t.Fatal("resumed run did not notify")
	}
}

func TestAgentControllerStopConsumesCompletion(t *testing.T) {
	started := make(chan struct{})
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "child", Prompt: "wait", SessionID: "child-session",
	}, "child.jsonl", func(ctx context.Context, _, _ string) (AgentCompletion, error) {
		close(started)
		<-ctx.Done()
		return AgentCompletion{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := store.Stop(launch.TaskID); err != nil {
		t.Fatal(err)
	}
	if commitCurrentNotification(store, launch.TaskID) {
		t.Fatal("stopped run claimed a completion notification")
	}
}

func TestAgentControllerCompletionSnapshotShape(t *testing.T) {
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{Description: "quick child", Prompt: "report"}, "child.jsonl", func(_ context.Context, _, _ string) (AgentCompletion, error) {
		return AgentCompletion{Result: "done", ToolUseCount: 2, TotalTokens: 9}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Wait(context.Background(), launch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != TaskCompleted || snapshot.Result != "done" || snapshot.ToolUseCount != 2 || snapshot.TotalTokens != 9 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if _, err := store.Stop(launch.TaskID); !errors.Is(err, errTaskNotRunning) {
		t.Fatalf("stop completed task error = %v", err)
	}
}

func TestAgentControllerResumeKeepsStableID(t *testing.T) {
	metadata := filepath.Join(t.TempDir(), "agent.json")
	var mu sync.Mutex
	var prompts []string
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "resumable child", Prompt: "first", SessionID: "child-session", MetadataPath: metadata,
	}, "child-events.jsonl", func(_ context.Context, _, prompt string) (AgentCompletion, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		return AgentCompletion{Result: "result: " + prompt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	status, err := store.QueueOrResume(context.Background(), launch.TaskID, "second")
	if err != nil || status != "resumed" {
		t.Fatalf("resume = %q %v", status, err)
	}
	snapshot, err := store.Wait(context.Background(), launch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TaskID != launch.TaskID || snapshot.Result != "result: second" || snapshot.Status != TaskCompleted {
		t.Fatalf("resumed snapshot = %+v", snapshot)
	}
	mu.Lock()
	gotPrompts := slices.Clone(prompts)
	mu.Unlock()
	if !slices.Equal(gotPrompts, []string{"first", "second"}) {
		t.Fatalf("prompts = %v", gotPrompts)
	}
	if _, err := os.Stat(metadata); err != nil {
		t.Fatalf("metadata missing: %v", err)
	}
}

func TestAgentControllerQueuesMessageAtRunBoundary(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	second := make(chan string, 1)
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "boundary child", Prompt: "first", SessionID: "child-session",
	}, "child-events.jsonl", func(_ context.Context, _, prompt string) (AgentCompletion, error) {
		if prompt == "first" {
			close(started)
			<-release
			return AgentCompletion{Result: "first done"}, nil
		}
		second <- prompt
		return AgentCompletion{Result: "second done"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	status, err := store.QueueOrResume(context.Background(), launch.TaskID, "follow-up")
	if err != nil || status != "queued" {
		t.Fatalf("queued = %q %v", status, err)
	}
	close(release)
	select {
	case prompt := <-second:
		if prompt != "follow-up" {
			t.Fatalf("follow-up prompt = %q", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("queued message did not start a follow-up run")
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
}

func TestAgentControllerRehydratesInterruptedAgent(t *testing.T) {
	metadata := filepath.Join(t.TempDir(), "agent.json")
	started := make(chan struct{})
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "restart child", Prompt: "first", SessionID: "child-session", MetadataPath: metadata,
	}, "child-events.jsonl", func(ctx context.Context, _, prompt string) (AgentCompletion, error) {
		if prompt == "first" {
			close(started)
			<-ctx.Done()
			return AgentCompletion{}, ctx.Err()
		}
		return AgentCompletion{Result: "resumed"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	store.Close()
	interrupted, ok := store.Get(launch.TaskID)
	if !ok || interrupted.Status != TaskInterrupted {
		t.Fatalf("after close = %+v %v", interrupted, ok)
	}

	restarted := NewAgentController()
	loaded, err := restarted.LoadMetadata(metadata, func(_ context.Context, _, prompt string) (AgentCompletion, error) {
		return AgentCompletion{Result: "resumed " + prompt}, nil
	})
	if err != nil || !loaded {
		t.Fatalf("load = %v %v", loaded, err)
	}
	if got, ok := restarted.Get(launch.TaskID); !ok || got.Status != TaskInterrupted {
		t.Fatalf("loaded task = %+v %v", got, ok)
	}
	if status, err := restarted.QueueOrResume(context.Background(), launch.TaskID, "after restart"); err != nil || status != "resumed" {
		t.Fatalf("restart resume = %q %v", status, err)
	}
	got, err := restarted.Wait(context.Background(), launch.TaskID)
	if err != nil || got.Status != TaskCompleted || !strings.Contains(got.Result, "after restart") {
		t.Fatalf("restart result = %+v %v", got, err)
	}
}

func TestAgentControllerConcurrentAgentsHaveIndependentLifecycle(t *testing.T) {
	const count = 4
	started := make(chan struct{}, count)
	release := make(chan struct{})
	store := NewAgentController()
	launches := make([]AgentLaunch, 0, count)
	run := func(_ context.Context, taskID, _ string) (AgentCompletion, error) {
		started <- struct{}{}
		<-release
		return AgentCompletion{Result: "done:" + taskID}, nil
	}
	for i := range count {
		launch, err := store.Start(context.Background(), AgentRequest{
			Description: "parallel child", Prompt: "work", SessionID: fmt.Sprintf("child-%d", i),
		}, fmt.Sprintf("child-%d.jsonl", i), run)
		if err != nil {
			t.Fatal(err)
		}
		launches = append(launches, launch)
	}
	for range count {
		<-started
	}
	close(release)
	seen := make(map[string]bool, count)
	for _, launch := range launches {
		got, err := store.Wait(context.Background(), launch.TaskID)
		if err != nil || got.Status != TaskCompleted || got.Result != "done:"+launch.TaskID {
			t.Fatalf("parallel task %s = %+v %v", launch.TaskID, got, err)
		}
		if seen[launch.TaskID] {
			t.Fatalf("duplicate task id %q", launch.TaskID)
		}
		seen[launch.TaskID] = true
	}
}

func TestAgentControllerStoppedAgentResumesAfterRunnerExits(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "stoppable child", Prompt: "first", SessionID: "child-session",
	}, "child-events.jsonl", func(ctx context.Context, _, prompt string) (AgentCompletion, error) {
		if prompt == "first" {
			close(started)
			<-ctx.Done()
			close(finished)
			return AgentCompletion{}, ctx.Err()
		}
		return AgentCompletion{Result: "resumed"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stopped, err := store.Stop(launch.TaskID)
	if err != nil || stopped.Status != TaskKilled {
		t.Fatalf("stop = %+v %v", stopped, err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled runner did not exit before Stop returned")
	}
	status, err := store.QueueOrResume(context.Background(), launch.TaskID, "continue")
	if err != nil || status != "resumed" {
		t.Fatalf("resume after stop = %q %v", status, err)
	}
	got, err := store.Wait(context.Background(), launch.TaskID)
	if err != nil || got.Status != TaskCompleted || got.TaskID != launch.TaskID || got.Result != "resumed" {
		t.Fatalf("resumed stopped task = %+v %v", got, err)
	}
}

func TestAgentControllerRemoveSessionForgetsTask(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "deleted child", Prompt: "wait", SessionID: "deleted-session", MetadataPath: filepath.Join(t.TempDir(), "agent.json"),
	}, "child-events.jsonl", func(ctx context.Context, _, _ string) (AgentCompletion, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return AgentCompletion{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	store.RemoveSession("deleted-session")
	if _, ok := store.Get(launch.TaskID); ok {
		t.Fatal("deleted session task remained in agent store")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("deleted child runner did not observe cancellation")
	}
}

func TestAgentControllerNotificationClaimIsPerRun(t *testing.T) {
	store := NewAgentController()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "notified child", Prompt: "first", SessionID: "child-session",
	}, "child-events.jsonl", func(_ context.Context, _, prompt string) (AgentCompletion, error) {
		return AgentCompletion{Result: prompt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !commitCurrentNotification(store, launch.TaskID) || commitCurrentNotification(store, launch.TaskID) {
		t.Fatal("same run was claimed more than once")
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	if status, err := store.QueueOrResume(context.Background(), launch.TaskID, "second"); err != nil || status != "resumed" {
		t.Fatalf("resume = %q %v", status, err)
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	if !commitCurrentNotification(store, launch.TaskID) || commitCurrentNotification(store, launch.TaskID) {
		t.Fatal("resumed run notification claim was not isolated")
	}
}

func TestAgentToolSchemaAndBackgroundResult(t *testing.T) {
	store := NewAgentController()
	defer store.Close()
	tool := agentTool{name: "spawn_agent", runtime: scopedAgentRuntime{AgentRuntime: fakeAgentRuntime{store: store}}}
	if err := tool.Validate(map[string]any{"task_name": "child", "message": "do it"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{"task_name": "child", "message": "do it", "model": "other/model"}, {"task_name": "child"}, {"task_name": "Child", "message": "do it"}, {"task_name": "child", "message": "do it", "run_in_background": true}} {
		if err := tool.Validate(args); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	result := tool.Execute(t.Context(), map[string]any{"task_name": "child", "message": "do it"})
	if result.IsError || result.Terminate {
		t.Fatalf("spawn must return async without terminating parent: %+v", result)
	}
	if len(result.Content) != 1 || !containsText(result.Content[0].Text, "task_name") {
		t.Fatalf("spawn result %v", result.Content)
	}
}

// A child agent is process-owned: cancelling the caller must not cancel it,
// because the Agent tool may promote it to background instead.
func TestAgentControllerChildSurvivesCallerCancel(t *testing.T) {
	store := NewAgentController()
	caller, cancelCaller := context.WithCancel(t.Context())
	release := make(chan struct{})
	launch, err := store.Start(caller, AgentRequest{Description: "child", Prompt: "wait"}, "child.jsonl", func(_ context.Context, _, _ string) (AgentCompletion, error) {
		<-release
		return AgentCompletion{Result: "late"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelCaller()
	if snapshot, _ := store.Get(launch.TaskID); snapshot.Status != TaskRunning {
		t.Fatalf("child did not outlive the caller: %s", snapshot.Status)
	}
	close(release)
	waitFor(t, func() bool {
		current, _ := store.Get(launch.TaskID)
		return current.Status == TaskCompleted
	}, "child never completed after the caller was cancelled")
}

func TestSpawnAgentReturnsBeforeChildCompletes(t *testing.T) {
	store := NewAgentController()
	defer store.Close()
	release := make(chan struct{})
	runtime := fakeAgentRuntime{store: store, run: func(_ context.Context, _, _ string) (AgentCompletion, error) {
		<-release
		return AgentCompletion{Result: "late"}, nil
	}}
	result := (agentTool{name: "spawn_agent", runtime: runtime}).Execute(t.Context(), map[string]any{"task_name": "child", "message": "work"})
	if result.IsError || result.Terminate {
		t.Fatalf("spawn: %+v", result)
	}
	tasks := stackedAgentSnapshots(store)
	if len(tasks) != 1 || tasks[0].Status != TaskRunning {
		t.Fatalf("spawn waited for completion: %+v", tasks)
	}
	close(release)
	waitFor(t, func() bool {
		tasks := stackedAgentSnapshots(store)
		return len(tasks) == 1 && tasks[0].Status == TaskCompleted
	}, "child never completed")
}

func TestSpawnedChildOutlivesParentObserver(t *testing.T) {
	store := NewAgentController()
	defer store.Close()
	started := make(chan struct{})
	runtime := fakeAgentRuntime{store: store, run: func(ctx context.Context, _, _ string) (AgentCompletion, error) {
		close(started)
		<-ctx.Done()
		return AgentCompletion{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	result := (agentTool{name: "spawn_agent", runtime: runtime}).Execute(ctx, map[string]any{"task_name": "child", "message": "work"})
	if result.IsError {
		t.Fatal(result)
	}
	<-started
	cancel()
	tasks := stackedAgentSnapshots(store)
	if tasks[0].Status != TaskRunning {
		t.Fatal("parent observer canceled child")
	}
	if _, err := store.Stop(tasks[0].TaskID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { tasks := stackedAgentSnapshots(store); return isTerminal(tasks[0].Status) }, "explicit interrupt did not stop child")
}

// messengerAgentRuntime is a runtime that can also receive SendMessage, so a
// Set build exposes both Agent and SendMessage.
type messengerAgentRuntime struct{ fakeAgentRuntime }

func (messengerAgentRuntime) SendAgentMessage(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
	return AgentMessageResult{Status: "steered"}, nil
}

func stackedAgentSnapshots(store *AgentController) []AgentSnapshot {
	store.mu.RLock()
	defer store.mu.RUnlock()
	out := make([]AgentSnapshot, 0, len(store.tasks))
	for _, task := range store.tasks {
		task.mu.Lock()
		out = append(out, task.snap)
		task.mu.Unlock()
	}
	return out
}

func waitFor(t *testing.T, ok func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

// Every child exposes the same schemas; admission controls active execution.
func TestAgentToolsAreExposedAtEveryDepth(t *testing.T) {
	runtime := fakeAgentRuntime{store: NewAgentController()}
	built := Set{CWD: t.TempDir(), Agent: runtime}.Build(Profile{})
	for _, name := range []string{"spawn_agent", "send_message", "followup_task", "wait_agent", "interrupt_agent", "list_agents"} {
		if pick(built, name) == nil {
			t.Fatalf("%s missing from %v", name, names(built))
		}
	}
	for _, name := range []string{"Agent", "TaskOutput", "TaskStop"} {
		if pick(built, name) != nil {
			t.Fatalf("retired tool %s exposed", name)
		}
	}
}

func TestSendMessageAndFollowupContracts(t *testing.T) {
	for _, name := range []string{"send_message", "followup_task"} {
		runtime := &recordingMessageRuntime{}
		tool := agentTool{name: name, runtime: runtime}
		if err := tool.Validate(map[string]any{"target": "/root/child", "message": "continue"}); err != nil {
			t.Fatal(err)
		}
		for _, args := range []map[string]any{{"message": "continue"}, {"target": "child"}, {"to": "child", "message": "continue"}} {
			if tool.Validate(args) == nil {
				t.Fatalf("accepted legacy/incomplete args: %+v", args)
			}
		}
		result := tool.Execute(t.Context(), map[string]any{"target": "/root/child", "message": "continue"})
		if result.IsError || runtime.message.Target != "/root/child" || runtime.message.TriggerTurn != (name == "followup_task") {
			t.Fatalf("%s: %+v %+v", name, result, runtime.message)
		}
	}
}

type recordingMessageRuntime struct {
	fakeAgentRuntime
	message AgentMessageRequest
}

func (r *recordingMessageRuntime) SendAgentMessage(_ context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
	r.message = req
	return AgentMessageResult{Status: "queued"}, nil
}

type fakeAgentRuntime struct {
	store *AgentController
	run   AgentRun
}

func (f fakeAgentRuntime) SpawnAgent(ctx context.Context, req AgentRequest) (AgentLaunch, error) {
	run := f.run
	if run == nil {
		run = func(_ context.Context, _, _ string) (AgentCompletion, error) {
			return AgentCompletion{Result: "done"}, nil
		}
	}
	return f.store.Start(ctx, req, "child.jsonl", run)
}

func (f fakeAgentRuntime) Get(key string) (AgentSnapshot, bool) { return f.store.Get(key) }
func (f fakeAgentRuntime) Wait(ctx context.Context, id string) (AgentSnapshot, error) {
	return f.store.Wait(ctx, id)
}

func (f fakeAgentRuntime) Stop(id string) (AgentSnapshot, error) { return f.store.Stop(id) }

func containsText(text, want string) bool {
	for i := 0; i+len(want) <= len(text); i++ {
		if text[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

func currentCompletionDelivered(store *AgentController, id string) bool {
	snapshot, _ := store.Get(id)
	return store.CompletionDelivered(types.CompletionIdentity{TaskID: id, Generation: snapshot.Generation})
}
func commitCurrentNotification(store *AgentController, id string) bool {
	snapshot, _ := store.Get(id)
	accepted, _ := store.CommitNotification(types.CompletionIdentity{TaskID: id, Generation: snapshot.Generation}, func() error { return nil })
	return accepted
}

func TestCompletionClaimsAreGenerationScopedAndRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	store := NewAgentController()
	defer store.Close()
	run := func(_ context.Context, _, prompt string) (AgentCompletion, error) {
		return AgentCompletion{Result: prompt}, nil
	}
	launch, err := store.Start(t.Context(), AgentRequest{SessionID: "child", ParentSessionID: "parent", Prompt: "first", MetadataPath: path}, "", run)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Wait(t.Context(), launch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueOrResume(t.Context(), launch.TaskID, "second"); err != nil {
		t.Fatal(err)
	}
	second, err := store.Wait(t.Context(), launch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 1 || second.Generation != 2 {
		t.Fatalf("generations: %+v %+v", first, second)
	}
	if !commitSnapshotNotification(store, first) {
		t.Fatal("old snapshot could not claim its own result")
	}
	id := types.CompletionIdentity{TaskID: launch.TaskID, Generation: second.Generation}
	persistErr := errors.New("persistence refused")
	if accepted, err := store.CommitNotification(id, func() error { return persistErr }); accepted || !errors.Is(err, persistErr) {
		t.Fatalf("failed persist committed: %v %v", accepted, err)
	}
	if store.CompletionDelivered(id) {
		t.Fatal("failed persistence consumed notification")
	}
	if accepted, err := store.CommitNotification(id, func() error { return nil }); !accepted || err != nil {
		t.Fatalf("new generation suppressed: %v %v", accepted, err)
	}
	if commitSnapshotNotification(store, second) {
		t.Fatal("notification and tool both claimed original delivery")
	}

	reloaded := NewAgentController()
	defer reloaded.Close()
	if loaded, err := reloaded.LoadMetadata(path, run); !loaded || err != nil {
		t.Fatalf("restore: %v %v", loaded, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	snapshot, err := reloaded.Wait(ctx, launch.TaskID)
	if err != nil || snapshot.Generation != 2 || snapshot.ParentSessionID != "parent" {
		t.Fatalf("restored completed Wait: %+v %v", snapshot, err)
	}
	for _, old := range []AgentSnapshot{first, second} {
		if commitSnapshotNotification(reloaded, old) {
			t.Fatalf("restored generation %d was claimed twice", old.Generation)
		}
	}
}

func TestAgentPendingInputIdentitySurvivesPromotion(t *testing.T) {
	store := NewAgentController()
	defer store.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	secondStarted := make(chan AgentSnapshot, 1)
	launch, err := store.Start(t.Context(), AgentRequest{SessionID: "child", Prompt: "first", MetadataPath: path}, "",
		func(ctx context.Context, id, prompt string) (AgentCompletion, error) {
			release := firstRelease
			if prompt == "second" {
				snapshot, _ := store.Get(id)
				secondStarted <- snapshot
				release = secondRelease
			}
			select {
			case <-release:
				return AgentCompletion{Result: prompt}, nil
			case <-ctx.Done():
				return AgentCompletion{}, ctx.Err()
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := store.QueueOrResume(t.Context(), launch.TaskID, "second"); err != nil || status != "queued" {
		t.Fatalf("queue: %s %v", status, err)
	}
	metadata, err := ReadAgentMetadata(path)
	if err != nil || len(metadata.Pending) != 1 || metadata.Pending[0].ClientRequestID == "" {
		t.Fatalf("pending identity: %+v %v", metadata.Pending, err)
	}
	close(firstRelease)
	select {
	case snapshot := <-secondStarted:
		if snapshot.Generation != 2 || snapshot.ClientRequestID != metadata.Pending[0].ClientRequestID {
			t.Fatalf("promoted identity: %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("follow-up did not start")
	}
	close(secondRelease)
}

func TestAgentMetadataMigrationKeepsConsumptionNotEnqueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	doc := map[string]any{"version": 1, "task_id": "child", "session_id": "child-session",
		"run_count": 3, "consumed_run": 2, "notified_run": 3, "pending": []string{"follow-up"}}
	if err := state.WriteJSON(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := ReadAgentMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Version != agentMetadataVersion || meta.Deliveries[2] != "tool" || meta.Deliveries[3] != "" ||
		len(meta.Pending) != 1 || meta.Pending[0].ClientRequestID == "" || meta.Pending[0].Prompt != "follow-up" {
		t.Fatalf("migration: %+v", meta)
	}
	raw, _, err := state.ReadFile(path, 1, nil)
	if err != nil {
		t.Fatalf("read-only migration changed disk version: %v", err)
	}
	if version, _ := state.Version(raw); version != 1 {
		t.Fatalf("read rewrote disk: %s", raw)
	}
	doc["version"] = 99
	if err := state.WriteJSON(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentMetadata(path); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatalf("newer document was accepted: %v", err)
	}
}

func (f fakeAgentRuntime) SendAgentMessage(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
	return AgentMessageResult{}, nil
}
func (f fakeAgentRuntime) WaitAgent(context.Context, string, time.Duration) (AgentWaitResult, error) {
	return AgentWaitResult{}, nil
}
func (f fakeAgentRuntime) ListAgents(string, string) ([]AgentView, error) { return nil, nil }
func (f fakeAgentRuntime) InterruptAgent(context.Context, string, string) (AgentView, error) {
	return AgentView{}, nil
}

func commitSnapshotNotification(store *AgentController, snapshot AgentSnapshot) bool {
	accepted, _ := store.CommitNotification(types.CompletionIdentity{TaskID: snapshot.TaskID, Generation: snapshot.Generation}, func() error { return nil })
	return accepted
}
