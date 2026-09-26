package tools

import (
	"context"
	"fmt"
	"strings"

	"ki/internal/loop"
	"ki/internal/search"
	"ki/internal/types"
)

const globPrompt = `Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- Use Glob together with Grep for open-ended searches that require multiple rounds of file discovery and content search
- Respects .gitignore by default; set respect_gitignore=false to include ignored files`

const defaultGlobLimit = 100

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
			"pattern":           map[string]any{"type": "string", "description": "The glob pattern to match files against"},
			"path":              map[string]any{"type": "string", "description": "The directory to search in. If not specified, the current working directory will be used. Must be a valid directory path if provided."},
			"respect_gitignore": map[string]any{"type": "boolean", "description": "Respect .gitignore rules. Defaults to true; set false to include ignored paths."},
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
	// Respect .gitignore by default so callers do not have to configure it;
	// only an explicit false opts back into the no-ignore search behavior.
	respectGitignore := true
	if v, ok := args["respect_gitignore"].(bool); ok {
		respectGitignore = v
	}
	result, err := (search.Engine{}).Glob(ctx, search.GlobRequest{
		Pattern:       pattern,
		Root:          root,
		MaxResults:    defaultGlobLimit,
		NoIgnore:      !respectGitignore,
		IncludeHidden: true,
		SortModified:  true,
	})
	if err != nil {
		return errRes(formatSearchError(err))
	}
	if len(result.Files) == 0 {
		return globResult("No files found", root, 0, false)
	}
	lines := make([]string, 0, len(result.Files)+1)
	for _, file := range result.Files {
		lines = append(lines, displaySearchPath(t.cwd, file))
	}
	output, note := limitSearchOutput(strings.Join(lines, "\n"), spillSafetyLimit)
	if result.Truncated {
		if note == "" {
			note = "\n\n[Results are truncated. Consider using a more specific path or pattern.]"
		} else {
			note += "\n\n[Results are truncated. Consider using a more specific path or pattern.]"
		}
	}
	return globResult(fmt.Sprintf("Found %d files\n%s%s", len(result.Files), output, note), root, len(result.Files), result.Truncated)
}

type globDetails struct {
	Root      string `json:"root"`
	Files     int    `json:"files"`
	Limit     int    `json:"limit"`
	Truncated bool   `json:"truncated"`
}

func globResult(text, root string, files int, truncated bool) loop.ToolResult {
	meta := fmt.Sprintf("\n\n[root: %s; files: %d; limit: %d; truncated: %t]", root, files, defaultGlobLimit, truncated)
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: text + meta}}, Details: globDetails{Root: root, Files: files, Limit: defaultGlobLimit, Truncated: truncated}}
}
