package agenttools

import (
	"context"
	"fmt"
	"time"

	"ki/internal/agent"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
)

// Build constructs session-scoped adapters around the host agent runtime.
func Build(runtime agent.Runtime, sessionID string) []toolapi.Tool {
	if runtime == nil {
		return nil
	}
	if sessionID != "" {
		runtime = scopedAgentRuntime{Runtime: runtime, sessionID: sessionID}
	}
	var out []toolapi.Tool
	for _, name := range catalog.AgentNames() {
		out = append(out, agentTool{name: name, runtime: runtime})
	}
	return out
}

type scopedAgentRuntime struct {
	agent.Runtime
	sessionID string
}

func (s scopedAgentRuntime) SpawnAgent(ctx context.Context, req agent.Request) (agent.Launch, error) {
	req.ParentSessionID = s.sessionID
	launch, err := s.Runtime.SpawnAgent(ctx, req)
	if err != nil {
		return agent.Launch{}, fmt.Errorf("spawn agent: %w", err)
	}
	return launch, nil
}

func (s scopedAgentRuntime) SendAgentMessage(ctx context.Context, req agent.MessageRequest) (agent.MessageResult, error) {
	req.SenderSessionID = s.sessionID
	return s.Runtime.SendAgentMessage(ctx, req)
}

func (s scopedAgentRuntime) WaitAgent(ctx context.Context, _ string, timeout time.Duration) (agent.WaitResult, error) {
	return s.Runtime.WaitAgent(ctx, s.sessionID, timeout)
}

func (s scopedAgentRuntime) ListAgents(_ string, prefix string) ([]agent.View, error) {
	return s.Runtime.ListAgents(s.sessionID, prefix)
}

func (s scopedAgentRuntime) InterruptAgent(ctx context.Context, _ string, target string) (agent.View, error) {
	return s.Runtime.InterruptAgent(ctx, s.sessionID, target)
}
