package builtin

import (
	"context"
	"testing"
	"time"

	"ki/internal/agent"
)

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

// A runtime-enabled build exposes the same schemas to every child; admission
// controls active execution rather than hiding tools by nesting depth.
func TestAgentToolsAreExposedWhenRuntimeIsAvailable(t *testing.T) {
	runtime := fakeAgentRuntime{store: agent.NewController()}
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
