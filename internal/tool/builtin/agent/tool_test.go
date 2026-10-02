package agenttools

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"ki/internal/agent"
)

type recordingAgentRuntime struct {
	request agent.Request
	timeout time.Duration
	waits   int
}

func (r *recordingAgentRuntime) SpawnAgent(_ context.Context, request agent.Request) (agent.Launch, error) {
	r.request = request
	return agent.Launch{TaskID: "agent-1", SessionID: "child-1", OutputFile: "child.jsonl"}, nil
}

func (*recordingAgentRuntime) SendAgentMessage(context.Context, agent.MessageRequest) (agent.MessageResult, error) {
	return agent.MessageResult{}, nil
}

func (r *recordingAgentRuntime) WaitAgent(_ context.Context, _ string, timeout time.Duration) (agent.WaitResult, error) {
	r.timeout = timeout
	r.waits++
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

func TestWaitAgentValidatesTimeoutBeforeDurationConversion(t *testing.T) {
	for _, value := range []any{0, 9999, 3600001, int64(math.MaxInt64), 10000.5, math.NaN(), math.Inf(1), json.Number("9999999999999999999999999")} {
		runtime := &recordingAgentRuntime{}
		result := (agentTool{name: "wait_agent", runtime: runtime}).Execute(t.Context(), map[string]any{"timeout_ms": value})
		if !result.IsError || runtime.waits != 0 {
			t.Errorf("timeout %v reached runtime: result=%+v, waits=%d", value, result, runtime.waits)
		}
	}
	for _, test := range []struct {
		value any
		want  time.Duration
	}{
		{nil, 30 * time.Second},
		{10000, 10 * time.Second},
		{int32(10000), 10 * time.Second},
		{json.Number("3600000"), time.Hour},
	} {
		runtime := &recordingAgentRuntime{}
		result := (agentTool{name: "wait_agent", runtime: runtime}).Execute(t.Context(), map[string]any{"timeout_ms": test.value})
		if result.IsError || runtime.waits != 1 || runtime.timeout != test.want {
			t.Errorf("timeout %v: result=%+v, waits=%d, duration=%v; want %v", test.value, result, runtime.waits, runtime.timeout, test.want)
		}
	}
}
