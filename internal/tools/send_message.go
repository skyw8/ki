package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ki/internal/loop"
)

const sendMessagePrompt = `Send a follow-up message to a running or completed background agent.

Use the stable agentId returned by Agent. A live agent receives the message after its current model/tool round; a completed, stopped, or interrupted agent resumes from its existing session transcript.`

type sendMessageTool struct{ messenger AgentMessenger }

func (sendMessageTool) Name() string        { return "SendMessage" }
func (sendMessageTool) Description() string { return "Send a follow-up message to an agent." }
func (sendMessageTool) Snippet() string     { return "Steer or resume an agent" }
func (sendMessageTool) Prompt() string      { return sendMessagePrompt }

func (sendMessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"to", "message"},
		"properties": map[string]any{
			"to":      map[string]any{"type": "string", "description": "The stable agentId returned by Agent."},
			"summary": map[string]any{"type": "string", "description": "A short summary of the follow-up message."},
			"message": map[string]any{"type": "string", "description": "The instruction to deliver to the agent."},
		},
	}
}

func (t sendMessageTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t sendMessageTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	if t.messenger == nil {
		return errRes("agent messaging is unavailable")
	}
	target := strings.TrimSpace(stringArg(args, "to", ""))
	message := strings.TrimSpace(stringArg(args, "message", ""))
	if target == "" {
		return errRes("to is required")
	}
	if message == "" {
		return errRes("message is required")
	}
	result, err := t.messenger.SendAgentMessage(ctx, AgentMessageRequest{
		Target: target, Summary: stringArg(args, "summary", ""), Message: message,
	})
	if err != nil {
		return errRes(err.Error())
	}
	b, err := json.Marshal(map[string]any{
		"success": true, "agentId": result.AgentID, "status": result.Status, "message": result.Message,
	})
	if err != nil {
		return errRes(fmt.Sprintf("encode SendMessage result: %v", err))
	}
	return okRes(string(b))
}
