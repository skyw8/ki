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
)

func TestAgentStoreBackgroundStop(t *testing.T) {
	store := NewAgentStore()
	started := make(chan struct{})
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "long child", Prompt: "wait", RunInBackground: true,
		SessionID: "child-session",
	}, filepath.Join(t.TempDir(), "child-events.jsonl"), func(ctx context.Context, _, _ string, _ bool) (AgentCompletion, error) {
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

func TestAgentStoreCompletionAndTaskOutputShape(t *testing.T) {
	store := NewAgentStore()
	launch, err := store.Start(context.Background(), AgentRequest{Description: "quick child", Prompt: "report"}, "child.jsonl", func(_ context.Context, _, _ string, _ bool) (AgentCompletion, error) {
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

func TestAgentStoreResumeKeepsStableID(t *testing.T) {
	metadata := filepath.Join(t.TempDir(), "agent.json")
	var mu sync.Mutex
	var prompts []string
	store := NewAgentStore()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "resumable child", Prompt: "first", RunInBackground: true,
		SessionID: "child-session", MetadataPath: metadata,
	}, "child-events.jsonl", func(_ context.Context, _, prompt string, _ bool) (AgentCompletion, error) {
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
	status, err := store.QueueOrResume(launch.TaskID, "second")
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
	gotPrompts := append([]string(nil), prompts...)
	mu.Unlock()
	if !slices.Equal(gotPrompts, []string{"first", "second"}) {
		t.Fatalf("prompts = %v", gotPrompts)
	}
	if _, err := os.Stat(metadata); err != nil {
		t.Fatalf("metadata missing: %v", err)
	}
}

func TestAgentStoreQueuesMessageAtRunBoundary(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	second := make(chan string, 1)
	store := NewAgentStore()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "boundary child", Prompt: "first", RunInBackground: true,
		SessionID: "child-session",
	}, "child-events.jsonl", func(_ context.Context, _, prompt string, _ bool) (AgentCompletion, error) {
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
	status, err := store.QueueOrResume(launch.TaskID, "follow-up")
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

func TestAgentStoreRehydratesInterruptedAgent(t *testing.T) {
	metadata := filepath.Join(t.TempDir(), "agent.json")
	started := make(chan struct{})
	store := NewAgentStore()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "restart child", Prompt: "first", RunInBackground: true,
		SessionID: "child-session", MetadataPath: metadata,
	}, "child-events.jsonl", func(ctx context.Context, _, prompt string, _ bool) (AgentCompletion, error) {
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

	restarted := NewAgentStore()
	loaded, err := restarted.LoadMetadata(metadata, func(_ context.Context, _, prompt string, _ bool) (AgentCompletion, error) {
		return AgentCompletion{Result: "resumed " + prompt}, nil
	})
	if err != nil || !loaded {
		t.Fatalf("load = %v %v", loaded, err)
	}
	if got, ok := restarted.Get(launch.TaskID); !ok || got.Status != TaskInterrupted {
		t.Fatalf("loaded task = %+v %v", got, ok)
	}
	if status, err := restarted.QueueOrResume(launch.TaskID, "after restart"); err != nil || status != "resumed" {
		t.Fatalf("restart resume = %q %v", status, err)
	}
	got, err := restarted.Wait(context.Background(), launch.TaskID)
	if err != nil || got.Status != TaskCompleted || !strings.Contains(got.Result, "after restart") {
		t.Fatalf("restart result = %+v %v", got, err)
	}
}

func TestAgentStoreConcurrentAgentsHaveIndependentLifecycle(t *testing.T) {
	const count = 4
	started := make(chan struct{}, count)
	release := make(chan struct{})
	store := NewAgentStore()
	launches := make([]AgentLaunch, 0, count)
	run := func(_ context.Context, taskID, _ string, _ bool) (AgentCompletion, error) {
		started <- struct{}{}
		<-release
		return AgentCompletion{Result: "done:" + taskID}, nil
	}
	for i := 0; i < count; i++ {
		launch, err := store.Start(context.Background(), AgentRequest{
			Description: "parallel child", Prompt: "work", RunInBackground: true,
			SessionID: fmt.Sprintf("child-%d", i),
		}, fmt.Sprintf("child-%d.jsonl", i), run)
		if err != nil {
			t.Fatal(err)
		}
		launches = append(launches, launch)
	}
	for i := 0; i < count; i++ {
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

func TestAgentStoreStoppedAgentResumesAfterRunnerExits(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	store := NewAgentStore()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "stoppable child", Prompt: "first", RunInBackground: true,
		SessionID: "child-session",
	}, "child-events.jsonl", func(ctx context.Context, _, prompt string, _ bool) (AgentCompletion, error) {
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
	status, err := store.QueueOrResume(launch.TaskID, "continue")
	if err != nil || status != "resumed" {
		t.Fatalf("resume after stop = %q %v", status, err)
	}
	got, err := store.Wait(context.Background(), launch.TaskID)
	if err != nil || got.Status != TaskCompleted || got.TaskID != launch.TaskID || got.Result != "resumed" {
		t.Fatalf("resumed stopped task = %+v %v", got, err)
	}
}

func TestAgentStoreRemoveSessionForgetsTask(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	store := NewAgentStore()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "deleted child", Prompt: "wait", RunInBackground: true,
		SessionID: "deleted-session", MetadataPath: filepath.Join(t.TempDir(), "agent.json"),
	}, "child-events.jsonl", func(ctx context.Context, _, _ string, _ bool) (AgentCompletion, error) {
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

func TestAgentStoreNotificationClaimIsPerRun(t *testing.T) {
	store := NewAgentStore()
	launch, err := store.Start(context.Background(), AgentRequest{
		Description: "notified child", Prompt: "first", RunInBackground: true,
		SessionID: "child-session",
	}, "child-events.jsonl", func(_ context.Context, _, prompt string, _ bool) (AgentCompletion, error) {
		return AgentCompletion{Result: prompt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !store.ClaimNotification(launch.TaskID) || store.ClaimNotification(launch.TaskID) {
		t.Fatal("same run was claimed more than once")
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	if status, err := store.QueueOrResume(launch.TaskID, "second"); err != nil || status != "resumed" {
		t.Fatalf("resume = %q %v", status, err)
	}
	if _, err := store.Wait(context.Background(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	if !store.ClaimNotification(launch.TaskID) || store.ClaimNotification(launch.TaskID) {
		t.Fatal("resumed run notification claim was not isolated")
	}
}

func TestAgentToolSchemaAndBackgroundResult(t *testing.T) {
	store := NewAgentStore()
	runtime := scopedAgentRuntime{AgentRuntime: fakeAgentRuntime{store: store}}
	tool := agentTool{runtime: runtime}
	params := tool.Parameters()
	if err := tool.Validate(map[string]any{"description": "child", "prompt": "do it", "run_in_background": true}); err != nil {
		t.Fatal(err)
	}
	if err := tool.Validate(map[string]any{"description": "child"}); err == nil {
		t.Fatal("missing prompt was accepted")
	}
	if params["properties"] == nil {
		t.Fatal("agent schema has no properties")
	}
	properties := params["properties"].(map[string]any)
	for _, name := range []string{"name", "team_name", "mode", "cwd", "isolation"} {
		if properties[name] != nil {
			t.Fatalf("Agent schema leaked team field %s", name)
		}
	}
	if strings.Contains(strings.ToLower(tool.Prompt()), "team") {
		t.Fatalf("Agent prompt leaked Agent Teams guidance: %q", tool.Prompt())
	}
	result := tool.Execute(context.Background(), map[string]any{
		"description": "child", "prompt": "do it", "run_in_background": true,
	})
	if result.IsError || !result.Terminate {
		t.Fatalf("background result = %+v", result)
	}
	if len(result.Content) != 1 || !containsText(result.Content[0].Text, "async_launched") {
		t.Fatalf("background content = %+v", result.Content)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(store.tasks) == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("agent task was not registered")
}

func TestAgentToolIsWithheldAtMaximumDepth(t *testing.T) {
	runtime := fakeAgentRuntime{store: NewAgentStore()}
	atLimit := Set{CWD: t.TempDir(), Agent: runtime, AgentDepth: MaxAgentDepth}.Build(Profile{})
	if pick(atLimit, "Agent") != nil {
		t.Fatalf("Agent was exposed at depth %d: %v", MaxAgentDepth, names(atLimit))
	}
	belowLimit := Set{CWD: t.TempDir(), Agent: runtime, AgentDepth: MaxAgentDepth - 1}.Build(Profile{})
	if pick(belowLimit, "Agent") == nil {
		t.Fatalf("Agent was not exposed below depth limit: %v", names(belowLimit))
	}
}

func TestSendMessageToolContract(t *testing.T) {
	tool := sendMessageTool{messenger: fakeMessenger{}}
	if err := tool.Validate(map[string]any{"to": "a-1", "message": "continue"}); err != nil {
		t.Fatal(err)
	}
	if err := tool.Validate(map[string]any{"message": "continue"}); err == nil {
		t.Fatal("missing target was accepted")
	}
	result := tool.Execute(context.Background(), map[string]any{"to": "a-1", "message": "continue"})
	if result.IsError || !containsText(result.Content[0].Text, "steered") {
		t.Fatalf("SendMessage result = %+v", result)
	}
}

type fakeMessenger struct{}

func (fakeMessenger) SendAgentMessage(_ context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
	if req.Target != "a-1" || req.Message != "continue" {
		return AgentMessageResult{}, errors.New("bad message")
	}
	return AgentMessageResult{AgentID: req.Target, Status: "steered", Message: "steered"}, nil
}

type fakeAgentRuntime struct{ store *AgentStore }

func (f fakeAgentRuntime) SpawnAgent(ctx context.Context, req AgentRequest) (AgentLaunch, error) {
	return f.store.Start(ctx, req, "child.jsonl", func(_ context.Context, _, _ string, _ bool) (AgentCompletion, error) {
		return AgentCompletion{Result: "done"}, nil
	})
}

func (f fakeAgentRuntime) Get(key string) (TaskSnapshot, bool) { return f.store.Get(key) }
func (f fakeAgentRuntime) Wait(ctx context.Context, id string) (TaskSnapshot, error) {
	return f.store.Wait(ctx, id)
}
func (f fakeAgentRuntime) Stop(id string) (TaskSnapshot, error) { return f.store.Stop(id) }

func containsText(text, want string) bool {
	for i := 0; i+len(want) <= len(text); i++ {
		if text[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
