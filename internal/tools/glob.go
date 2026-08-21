package tools

import (
	"context"
	"strings"

	"ki/internal/loop"
	"ki/internal/search"
)

const globPrompt = `Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- Use Glob together with Grep for open-ended searches that require multiple rounds of file discovery and content search`

const defaultGlobLimit = 100
const globMaxOutput = 100_000

type globTool struct{ cwd string }

func (globTool) Name() string        { return "Glob" }
func (globTool) Description() string { return "Find files by name pattern or wildcard." }
func (globTool) Snippet() string     { return "Find files by glob pattern" }
func (globTool) Prompt() string      { return globPrompt }

func (globTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"pattern"},
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "The glob pattern to match files against"},
			"path":    map[string]any{"type": "string", "description": "The directory to search in. If not specified, the current working directory will be used. Must be a valid directory path if provided."},
		},
	}
}

func (globTool) Validate(args map[string]any) error {
	return validateArgs(globTool{}.Parameters(), "Glob", args)
}

func (t globTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return errRes("pattern is required")
	}
	root := resolve(t.cwd, stringArg(args, "path", "."))
	if absoluteRoot, relativePattern, ok := splitAbsoluteGlob(pattern); ok {
		root = absoluteRoot
		pattern = relativePattern
	}
	result, err := (search.Engine{}).Glob(ctx, search.GlobRequest{
		Pattern:       pattern,
		Root:          root,
		MaxResults:    defaultGlobLimit,
		NoIgnore:      true,
		IncludeHidden: true,
		SortModified:  true,
	})
	if err != nil {
		return errRes(formatSearchError(err))
	}
	if len(result.Files) == 0 {
		return okRes("No files found")
	}
	lines := make([]string, 0, len(result.Files)+1)
	for _, file := range result.Files {
		lines = append(lines, displaySearchPath(t.cwd, file))
	}
	output, note := limitSearchOutput(strings.Join(lines, "\n"), globMaxOutput)
	if result.Truncated {
		if note == "" {
			note = "\n\n[Results are truncated. Consider using a more specific path or pattern.]"
		} else {
			note += "\n\n[Results are truncated. Consider using a more specific path or pattern.]"
		}
	}
	return okRes(output + note)
}
