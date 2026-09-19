package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ki/internal/loop"
)

const sendMessagePrompt = `Send a message to another agent.

Targets:
- "parent" (default): the session that spawned this one. A subagent uses this to ask its caller something while working; its final result already travels back on its own.
- "main": the top-level session at the root of this session chain.
- an agentId returned by Agent: steer or resume that specific child.

A live agent receives the message after its current model/tool round; a completed, stopped, or interrupted agent resumes from its existing session transcript.`

type sendMessageTool struct{ messenger AgentMessenger }

func (sendMessageTool) Name() string        { return "SendMessage" }
func (sendMessageTool) Description() string { return "Send a follow-up message to an agent." }
func (sendMessageTool) Snippet() string     { return "Steer or resume an agent" }
func (sendMessageTool) Prompt() string      { return sendMessagePrompt }

func (sendMessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"message"},
		"properties": map[string]any{
			"to":      map[string]any{"type": "string", "description": "Recipient: \"parent\" (default), \"main\", or an agentId returned by Agent."},
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
	target := cmp.Or(strings.TrimSpace(stringArg(args, "to", "")), AgentTargetParent)
	message := strings.TrimSpace(stringArg(args, "message", ""))
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
