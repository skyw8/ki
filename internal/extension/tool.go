package extension

import (
	"context"
	"fmt"

	"ki/internal/loop"
)

type sidecarTool struct {
	client    *rpcClient
	sessionID string
	spec      ToolSpec
}

func (t sidecarTool) Name() string        { return t.spec.Name }
func (t sidecarTool) Description() string { return t.spec.Description }
func (t sidecarTool) Prompt() string      { return t.spec.Description }
func (t sidecarTool) Snippet() string {
	if t.spec.Snippet != "" {
		return t.spec.Snippet
	}
	return t.spec.Description
}
func (t sidecarTool) Parameters() map[string]any { return t.spec.Parameters }

func (t sidecarTool) Validate(args map[string]any) error {
	if msg := loop.SchemaErrors(t.Parameters(), t.spec.Name, args); msg != "" {
		return fmt.Errorf("%w: %s", errRPC, msg)
	}
	return nil
}

func (t sidecarTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	return t.client.executeTool(withSessionID(ctx, t.sessionID), t.spec, "", t.spec.Name, args, nil)
}

func (t sidecarTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	return t.client.executeTool(withSessionID(ctx, t.sessionID), t.spec, "", t.spec.Name, args, emit)
}

func toolsFromRegistration(c *rpcClient, sessionID string) []loop.Tool {
	if c == nil || !hasKind(c.capabilities, CapTool) {
		return nil
	}
	return toolsFromSpecs(c, sessionID, c.registration.Tools)
}

func toolsFromSpecs(c *rpcClient, sessionID string, specs []ToolSpec) []loop.Tool {
	if c == nil || !hasKind(c.capabilities, CapTool) {
		return nil
	}
	var out []loop.Tool
	for _, spec := range specs {
		if reservedToolNames[spec.Name] {
			continue
		}
		out = append(out, sidecarTool{client: c, sessionID: sessionID, spec: spec})
	}
	return out
}
