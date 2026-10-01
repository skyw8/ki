package filetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/tool/builtin/internal/support"
)

type writeTool struct {
	cwd       string
	mutations *MutationQueue
}

func (writeTool) Name() string { return catalog.Write }

func (writeTool) Description() string { return "Write a file to the local filesystem." }

func (writeTool) Snippet() string { return "Create or overwrite files" }

func (writeTool) Prompt() string {
	return `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- Prefer the Edit tool for modifying existing files — it only sends the diff. Only use this tool to create new files or for complete rewrites.
- NEVER create documentation files (*.md) or README files unless explicitly requested by the User.
- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.`
}

func (writeTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"file_path", "content"},
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "The absolute path to the file to write (must be absolute, not relative)"},
			"content":   map[string]any{"type": "string", "description": "The content to write to the file"},
		},
	}
}

func (t writeTool) Validate(args map[string]any) error {
	return support.ValidateArgs(t.Parameters(), t.Name(), args)
}

func (t writeTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	path, _ := args["file_path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return support.Error("file_path is required")
	}
	abs := resolve(t.cwd, path)
	queue := t.mutations
	if queue == nil {
		queue = NewMutationQueue()
	}
	release, err := queue.LockPaths(ctx, abs)
	if err != nil {
		return support.Error("Write aborted")
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return support.Error("Write aborted")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return support.Error(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return support.Error("Write aborted")
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		return support.Error(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return support.Error("Write aborted")
	}
	return support.Text(fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path))
}
