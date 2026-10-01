package extension

import (
	"context"
	"fmt"

	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
)

type sidecarTool struct {
	client    *rpcClient
	sessionID string
	spec      ToolSpec
}

func (t sidecarTool) Name() string      { return toolapi.MustCanonical(t.spec.Name) }
func (t sidecarTool) Aliases() []string { names, _ := toolapi.Aliases(t.spec.Name); return names }

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
	if msg := toolapi.SchemaErrors(t.Parameters(), t.spec.Name, args); msg != "" {
		return fmt.Errorf("%w: %s", errRPC, msg)
	}
	return nil
}

func (t sidecarTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	return t.client.executeTool(withSessionID(ctx, t.sessionID), t.spec, "", t.spec.Name, args, nil)
}

func (t sidecarTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) toolapi.Result {
	return t.client.executeTool(withSessionID(ctx, t.sessionID), t.spec, "", t.spec.Name, args, emit)
}

func toolsFromRegistration(c *rpcClient, sessionID string) []toolapi.Tool {
	if c == nil || !hasKind(c.capabilities, CapTool) {
		return nil
	}
	return toolsFromSpecs(c, sessionID, c.registration.Tools)
}

func toolsFromSpecs(c *rpcClient, sessionID string, specs []ToolSpec) []toolapi.Tool {
	if c == nil || !hasKind(c.capabilities, CapTool) {
		return nil
	}
	var out []toolapi.Tool
	for _, spec := range specs {
		if catalog.IsReserved(spec.Name) {
			continue
		}
		out = append(out, sidecarTool{client: c, sessionID: sessionID, spec: spec})
	}
	return out
}
