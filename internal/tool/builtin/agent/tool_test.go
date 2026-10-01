package agenttools

import (
	"context"
	"strings"
	"testing"
	"time"

	"ki/internal/agent"
)

type recordingAgentRuntime struct {
	request agent.Request
}

func (r *recordingAgentRuntime) SpawnAgent(_ context.Context, request agent.Request) (agent.Launch, error) {
	r.request = request
	return agent.Launch{TaskID: "agent-1", SessionID: "child-1", OutputFile: "child.jsonl"}, nil
}

func (*recordingAgentRuntime) SendAgentMessage(context.Context, agent.MessageRequest) (agent.MessageResult, error) {
	return agent.MessageResult{}, nil
}

func (*recordingAgentRuntime) WaitAgent(context.Context, string, time.Duration) (agent.WaitResult, error) {
	return agent.WaitResult{}, nil
}

func (*recordingAgentRuntime) ListAgents(string, string) ([]agent.View, error) { return nil, nil }

func (*recordingAgentRuntime) InterruptAgent(context.Context, string, string) (agent.View, error) {
	return agent.View{}, nil
}

func TestAgentToolContextInheritanceDefault(t *testing.T) {
	runtime := &recordingAgentRuntime{}
	result := (agentTool{name: "spawn_agent", runtime: runtime}).Execute(context.Background(), map[string]any{
		"task_name": "inspect_repository",
		"message":   "Inspect the repository.",
	})
	if result.IsError {
		t.Fatalf("execute: %+v", result)
	}
	if runtime.request.ForkTurns != "all" {
		t.Fatal("spawn_agent did not inherit all completed context by default")
	}

	result = (agentTool{name: "spawn_agent", runtime: runtime}).Execute(context.Background(), map[string]any{
		"task_name":  "continue_reasoning",
		"message":    "Continue the prior reasoning.",
		"fork_turns": "none",
	})
	if result.IsError {
		t.Fatalf("execute explicit inherit: %+v", result)
	}
	if runtime.request.ForkTurns != "none" {
		t.Fatal("spawn_agent ignored fork_turns=none")
	}
}

func TestAgentToolPromptExplainsContextInheritance(t *testing.T) {
	tool := agentTool{name: "spawn_agent"}
	if !strings.Contains(tool.Prompt(), "returns immediately") || !strings.Contains(tool.Parameters()["properties"].(map[string]any)["fork_turns"].(map[string]any)["description"].(string), "all (default)") {
		t.Fatal("spawn prompt/schema omitted async and fork defaults")
	}
}
