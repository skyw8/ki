package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"ki/internal/loop"
)

type editTool struct{ cwd string }

func (editTool) Name() string        { return "Edit" }
func (editTool) Description() string { return "A tool for editing files" }
func (editTool) Snippet() string {
	return "Make precise file edits with exact text replacement"
}
func (editTool) Prompt() string {
	return `Performs exact string replacements in files.

Usage:
- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.
- Only use emojis if the user explicitly requests it. Avoid adding emojis to files unless asked.
- The edit will FAIL if old_string is not unique in the file. Either provide a larger string with more surrounding context to make it unique or use replace_all to change every instance of old_string.
- Use replace_all for replacing and renaming strings across the file. This parameter is useful if you want to rename a variable for instance.`
}
func (editTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"file_path", "old_string", "new_string"},
		"properties": map[string]any{
			"file_path":   map[string]any{"type": "string", "description": "The absolute path to the file to modify"},
			"old_string":  map[string]any{"type": "string", "description": "The text to replace"},
			"new_string":  map[string]any{"type": "string", "description": "The text to replace it with (must be different from old_string)"},
			"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences of old_string (default false)"},
		},
	}
}

func (t editTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t editTool) Execute(_ context.Context, args map[string]any) loop.ToolResult {
	path, _ := args["file_path"].(string)
	oldS, _ := args["old_string"].(string)
	newS, _ := args["new_string"].(string)
	if path == "" || oldS == "" {
		return errRes("file_path and old_string are required")
	}
	if oldS == newS {
		return errRes("No changes to make: old_string and new_string are exactly the same.")
	}
	abs := resolve(t.cwd, path)
	//nolint:gosec // abs is normalized and confined to the requested tool path.
	data, err := os.ReadFile(abs)
	if err != nil {
		return errRes(fmt.Sprintf("Could not edit file: %s. %s", path, err.Error()))
	}
	text := string(data)
	count := strings.Count(text, oldS)
	if count == 0 {
		return errRes(fmt.Sprintf("Could not find old_string in %s. The old_string must match exactly including all whitespace and newlines.", path))
	}
	all := false
	switch v := args["replace_all"].(type) {
	case bool:
		all = v
	case string:
		all = v == "true"
	}
	if count > 1 && !all {
		return errRes(fmt.Sprintf("Found %d occurrences of old_string in %s. Each old_string must be unique. Use replace_all or provide more context.", count, path))
	}
	var next string
	n := 1
	if all {
		n = -1
	}
	next = strings.Replace(text, oldS, newS, n)
	// resolve validates the tool path against the session working directory.
	//nolint:gosec // writing the user-requested file is the Edit tool contract
	if err := os.WriteFile(abs, []byte(next), 0o600); err != nil {
		return errRes(err.Error())
	}
	repl := 1
	if all {
		repl = count
	}
	return okRes(fmt.Sprintf("Successfully replaced %d block(s) in %s.", repl, path))
}
