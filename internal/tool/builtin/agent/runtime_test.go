package agenttools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ki/internal/agent"
	toolapi "ki/internal/tool"
)

func TestAgentToolSchemaAndBackgroundResult(t *testing.T) {
	store := agent.NewController()
	defer store.Close()
	tool := agentTool{name: "spawn_agent", runtime: scopedAgentRuntime{Runtime: fakeAgentRuntime{store: store}}}
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

func TestSpawnAgentReturnsBeforeChildCompletes(t *testing.T) {
	store := agent.NewController()
	defer store.Close()
	release := make(chan struct{})
	runtime := fakeAgentRuntime{store: store, run: func(_ context.Context, _, _ string) (agent.Completion, error) {
		<-release
		return agent.Completion{Result: "late"}, nil
	}}
	result := (agentTool{name: "spawn_agent", runtime: runtime}).Execute(t.Context(), map[string]any{"task_name": "child", "message": "work"})
	if result.IsError || result.Terminate {
		t.Fatalf("spawn: %+v", result)
	}
	tasks := agentSnapshots(t, store, result)
	if len(tasks) != 1 || tasks[0].Status != agent.Running {
		t.Fatalf("spawn waited for completion: %+v", tasks)
	}
	close(release)
	waitFor(t, func() bool {
		tasks := agentSnapshots(t, store, result)
		return len(tasks) == 1 && tasks[0].Status == agent.Completed
	}, "child never completed")
}

func TestSpawnedChildOutlivesParentObserver(t *testing.T) {
	store := agent.NewController()
	defer store.Close()
	started := make(chan struct{})
	runtime := fakeAgentRuntime{store: store, run: func(ctx context.Context, _, _ string) (agent.Completion, error) {
		close(started)
		<-ctx.Done()
		return agent.Completion{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	result := (agentTool{name: "spawn_agent", runtime: runtime}).Execute(ctx, map[string]any{"task_name": "child", "message": "work"})
	if result.IsError {
		t.Fatal(result)
	}
	<-started
	cancel()
	tasks := agentSnapshots(t, store, result)
	if tasks[0].Status != agent.Running {
		t.Fatal("parent observer canceled child")
	}
	if _, err := store.Stop(tasks[0].TaskID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { tasks := agentSnapshots(t, store, result); return tasks[0].Status == agent.Killed }, "explicit interrupt did not stop child")
}

// messengerAgentRuntime is a runtime that can also receive SendMessage, so a
// Set build exposes both Agent and SendMessage.
type messengerAgentRuntime struct{ fakeAgentRuntime }

func (messengerAgentRuntime) SendAgentMessage(context.Context, agent.MessageRequest) (agent.MessageResult, error) {
	return agent.MessageResult{Status: "steered"}, nil
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
	message agent.MessageRequest
}

func (r *recordingMessageRuntime) SendAgentMessage(_ context.Context, req agent.MessageRequest) (agent.MessageResult, error) {
	r.message = req
	return agent.MessageResult{Status: "queued"}, nil
}

type fakeAgentRuntime struct {
	store *agent.Controller
	run   agent.Run
}

func (f fakeAgentRuntime) SpawnAgent(ctx context.Context, req agent.Request) (agent.Launch, error) {
	run := f.run
	if run == nil {
		run = func(_ context.Context, _, _ string) (agent.Completion, error) {
			return agent.Completion{Result: "done"}, nil
		}
	}
	return f.store.Start(ctx, req, "child.jsonl", run)
}

func (f fakeAgentRuntime) Get(key string) (agent.Snapshot, bool) { return f.store.Get(key) }

func (f fakeAgentRuntime) Wait(ctx context.Context, id string) (agent.Snapshot, error) {
	return f.store.Wait(ctx, id)
}

func (f fakeAgentRuntime) Stop(id string) (agent.Snapshot, error) { return f.store.Stop(id) }

func containsText(text, want string) bool {
	for i := 0; i+len(want) <= len(text); i++ {
		if text[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

func (f fakeAgentRuntime) SendAgentMessage(context.Context, agent.MessageRequest) (agent.MessageResult, error) {
	return agent.MessageResult{}, nil
}

func (f fakeAgentRuntime) WaitAgent(context.Context, string, time.Duration) (agent.WaitResult, error) {
	return agent.WaitResult{}, nil
}

func (f fakeAgentRuntime) ListAgents(string, string) ([]agent.View, error) { return nil, nil }

func (f fakeAgentRuntime) InterruptAgent(context.Context, string, string) (agent.View, error) {
	return agent.View{}, nil
}

// agentSnapshots inspects the public identity returned by the tool.
func agentSnapshots(t *testing.T, store *agent.Controller, result toolapi.Result) []agent.Snapshot {
	t.Helper()
	var launch struct {
		ID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &launch); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := store.Get(launch.ID)
	if !ok {
		t.Fatalf("unknown agent %q", launch.ID)
	}
	return []agent.Snapshot{snapshot}
}
