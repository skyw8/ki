package tools

import (
	"context"
	"strings"
	"testing"
)

type recordingAgentRuntime struct {
	request AgentRequest
}

func (r *recordingAgentRuntime) SpawnAgent(_ context.Context, request AgentRequest) (AgentLaunch, error) {
	r.request = request
	return AgentLaunch{TaskID: "agent-1", SessionID: "child-1", OutputFile: "child.jsonl"}, nil
}

func (*recordingAgentRuntime) Get(string) (TaskSnapshot, bool) {
	return TaskSnapshot{}, false
}

func (*recordingAgentRuntime) Wait(context.Context, string) (TaskSnapshot, error) {
	return TaskSnapshot{}, nil
}

func (*recordingAgentRuntime) Stop(string) (TaskSnapshot, error) {
	return TaskSnapshot{}, nil
}

func (*recordingAgentRuntime) ClaimResult(TaskSnapshot) bool { return true }

func (*recordingAgentRuntime) Background(string) (TaskSnapshot, error) {
	return TaskSnapshot{}, nil
}

func TestAgentToolContextInheritanceDefault(t *testing.T) {
	runtime := &recordingAgentRuntime{}
	result := (agentTool{runtime: runtime}).Execute(context.Background(), map[string]any{
		"description":       "inspect repository",
		"prompt":            "Inspect the repository.",
		"run_in_background": true,
	})
	if result.IsError {
		t.Fatalf("execute: %+v", result)
	}
	if runtime.request.InheritContext {
		t.Fatal("Agent inherited parent context by default")
	}

	result = (agentTool{runtime: runtime}).Execute(context.Background(), map[string]any{
		"description":       "continue reasoning",
		"prompt":            "Continue the prior reasoning.",
		"inherit_context":   true,
		"run_in_background": true,
	})
	if result.IsError {
		t.Fatalf("execute explicit inherit: %+v", result)
	}
	if !runtime.request.InheritContext {
		t.Fatal("Agent ignored explicit context inheritance")
	}
}

func TestAgentToolPromptExplainsContextInheritance(t *testing.T) {
	if !strings.Contains(agentPrompt, "By default the child starts with a clean conversation") ||
		!strings.Contains(agentPrompt, "repository research, scoped implementation, testing, and independent review") {
		t.Fatal("Agent prompt does not explain the clean-context default")
	}
}
