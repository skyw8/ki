package server

import (
	"testing"

	"ki/internal/agent"
	"ki/internal/loop"
	"ki/internal/types"
)

func TestAgentProgressProjectionKeepsAssistantUsageAndExcludesUserContent(t *testing.T) {
	usage := &types.Usage{Input: 5, Output: 2, CacheRead: 10}
	message := &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "finished"}}, Usage: usage}
	event := loop.Event{Type: loop.MessageEnd, Message: message, RunID: "run", Timestamp: 123}
	progress := projectAgentProgress(event)
	if progress.Type != agent.MessageCompleted || progress.Text != "finished" || progress.Usage != usage || progress.RunID != "run" || progress.Timestamp != 123 {
		t.Fatalf("assistant progress lost attribution or usage: %+v", progress)
	}
	message.Role = "user"
	progress = projectAgentProgress(event)
	if progress.Type != agent.MessageCompleted || progress.Text != "" || progress.Usage != nil {
		t.Fatalf("user content entered agent result or statistics: %+v", progress)
	}
	progress = projectAgentProgress(loop.Event{Type: loop.MessageUpdate, Message: message})
	if progress.Type != 0 || progress.Text != "" || progress.Usage != nil {
		t.Fatalf("streaming event entered durable progress: %+v", progress)
	}
}
