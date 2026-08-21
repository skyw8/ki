package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"ki/internal/loop"
	"ki/internal/search"
)

const grepPrompt = `A powerful search tool built on ripgrep

Usage:
- ALWAYS use Grep for search tasks. NEVER invoke grep or rg as a Bash command. The Grep tool is optimized for correct permissions and access.
- Supports full regex syntax (e.g., "log.*Error", "function\s+\w+")
- Filter files with the glob parameter (e.g., "*.js", "**/*.tsx") or the type parameter (e.g., "js", "py", "rust")
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), and "count" shows match counts
- Use Glob together with Grep for open-ended searches that require multiple rounds of file discovery and content search
- Pattern syntax uses ripgrep, not grep; literal braces need escaping (use "interface\{\}" to find "interface{}" in Go code)
- Multiline matching is disabled by default. For cross-line patterns like "struct \{[\s\S]*?field", use multiline: true`

const defaultGrepHeadLimit = 250
const grepMaxOutput = 20_000
const grepMaxLineLength = 500

type grepTool struct{ cwd string }

func (grepTool) Name() string        { return "Grep" }
func (grepTool) Description() string { return "Search file contents using ripgrep." }
func (grepTool) Snippet() string     { return "Search file contents with regex (ripgrep)" }
func (grepTool) Prompt() string      { return grepPrompt }

func (grepTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"pattern"},
		"properties": map[string]any{
			"pattern":     map[string]any{"type": "string", "description": "The regular expression pattern to search for in file contents"},
			"path":        map[string]any{"type": "string", "description": "File or directory to search in (rg PATH). Defaults to current working directory."},
			"glob":        map[string]any{"type": "string", "description": "Glob pattern to filter files (e.g. \"*.js\", \"*.{ts,tsx}\") - maps to rg --glob"},
			"output_mode": map[string]any{"type": "string", "enum": []any{"content", "files_with_matches", "count"}, "description": "Output mode: \"content\" shows matching lines, \"files_with_matches\" shows only file paths (default), or \"count\" shows match counts."},
			"-B":          map[string]any{"type": "number", "description": "Number of lines to show before each match. Requires output_mode: content."},
			"-A":          map[string]any{"type": "number", "description": "Number of lines to show after each match. Requires output_mode: content."},
			"-C":          map[string]any{"type": "number", "description": "Alias for context."},
			"context":     map[string]any{"type": "number", "description": "Number of lines to show before and after each match. Requires output_mode: content."},
			"-n":          map[string]any{"type": "boolean", "description": "Show line numbers in content output. Defaults to true."},
			"-i":          map[string]any{"type": "boolean", "description": "Case insensitive search."},
			"type":        map[string]any{"type": "string", "description": "File type to search, such as js, py, rust, or go."},
			"head_limit":  map[string]any{"type": "number", "description": "Limit output to the first N lines or entries. Defaults to 250. Pass 0 for unlimited."},
			"offset":      map[string]any{"type": "number", "description": "Skip the first N lines or entries before applying head_limit. Defaults to 0."},
			"multiline":   map[string]any{"type": "boolean", "description": "Enable multiline mode where patterns can span lines. Defaults to false."},
		},
	}
}

func (t grepTool) Validate(args map[string]any) error {
	if err := validateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	if mode, ok := args["output_mode"].(string); ok && mode != "content" && mode != "files_with_matches" && mode != "count" {
		return fmt.Errorf("%w: output_mode must be content, files_with_matches, or count", errToolExecution)
	}
	return nil
}

func (t grepTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return errRes("pattern is required")
	}
	root := resolve(t.cwd, stringArg(args, "path", "."))
	mode := stringArg(args, "output_mode", "files_with_matches")
	if mode != "content" && mode != "files_with_matches" && mode != "count" {
		return errRes("output_mode must be content, files_with_matches, or count")
	}

	headLimit := defaultGrepHeadLimit
	if v, ok := asInt(args["head_limit"]); ok {
		if v < 0 {
			return errRes("head_limit must be non-negative")
		}
		headLimit = v
	}
	offset := 0
	if v, ok := asInt(args["offset"]); ok {
		if v < 0 {
			return errRes("offset must be non-negative")
		}
		offset = v
	}

	before, after := contextLines(args)
	showLine := true
	if v, ok := args["-n"].(bool); ok {
		showLine = v
	}
	multiline, _ := args["multiline"].(bool)
	ignoreCase, _ := args["-i"].(bool)
	globPatterns := splitGlobArguments(stringArg(args, "glob", ""))
	maxResults := 0
	if mode == "content" && headLimit > 0 {
		maxResults = offset + headLimit
	}

	result, err := (search.Engine{}).Grep(ctx, search.GrepRequest{
		Pattern:       pattern,
		Root:          root,
		Glob:          globPatterns,
		Type:          stringArg(args, "type", ""),
		IgnoreCase:    ignoreCase,
		Before:        before,
		After:         after,
		Context:       max(before, after),
		ShowLine:      showLine,
		Multiline:     multiline,
		OutputMode:    mode,
		MaxResults:    maxResults,
		IncludeHidden: true,
	})
	if err != nil {
		return errRes(formatSearchError(err))
	}

	output, err := formatGrepResult(result, mode, t.cwd, offset, headLimit, before, after, showLine)
	if err != nil {
		return errRes(err.Error())
	}
	return okRes(output)
}

func contextLines(args map[string]any) (before, after int) {
	if v, ok := asInt(args["context"]); ok && v > 0 {
		return v, v
	}
	if v, ok := asInt(args["-C"]); ok && v > 0 {
		return v, v
	}
	if v, ok := asInt(args["-B"]); ok && v > 0 {
		before = v
	}
	if v, ok := asInt(args["-A"]); ok && v > 0 {
		after = v
	}
	return before, after
}

func formatGrepResult(result search.GrepResult, mode, cwd string, offset, headLimit, before, after int, showLine bool) (string, error) {
	switch mode {
	case "content":
		if len(result.Matches) == 0 {
			return "No matches found", nil
		}
		start, end := pageBounds(len(result.Matches), offset, headLimit)
		var lines []string
		lineWasTruncated := false
		for _, match := range result.Matches[start:end] {
			block, truncated, err := formatMatch(match, cwd, before, after, showLine)
			if err != nil {
				return "", err
			}
			lines = append(lines, block...)
			lineWasTruncated = lineWasTruncated || truncated
		}
		output := strings.Join(lines, "\n")
		output, note := limitSearchOutput(output, grepMaxOutput)
		if result.Truncated {
			note = appendSearchNote(note, fmt.Sprintf("Showing results with pagination = limit: %d", headLimit))
		}
		if lineWasTruncated {
			note = appendSearchNote(note, fmt.Sprintf("Some lines truncated to %d chars. Use Read to see full lines", grepMaxLineLength))
		}
		return output + note, nil

	case "count":
		counts := append([]search.Count(nil), result.Counts...)
		start, end := pageBounds(len(counts), offset, headLimit)
		lines := make([]string, 0, end-start)
		total := 0
		for _, count := range counts[start:end] {
			lines = append(lines, fmt.Sprintf("%s:%d", displaySearchPath(cwd, count.Path), count.Count))
			total += count.Count
		}
		if len(lines) == 0 {
			return "No matches found", nil
		}
		output, note := limitSearchOutput(strings.Join(lines, "\n"), grepMaxOutput)
		note = appendSearchNote(note, fmt.Sprintf("Found %d total occurrences across %d files.", total, len(lines)))
		return output + note, nil

	default:
		files := append([]string(nil), result.Files...)
		sort.Slice(files, func(i, j int) bool {
			left, leftErr := os.Stat(files[i])
			right, rightErr := os.Stat(files[j])
			if leftErr == nil && rightErr == nil && !left.ModTime().Equal(right.ModTime()) {
				return left.ModTime().After(right.ModTime())
			}
			return files[i] < files[j]
		})
		start, end := pageBounds(len(files), offset, headLimit)
		if start == end {
			return "No files found", nil
		}
		lines := make([]string, 0, end-start+1)
		for _, file := range files[start:end] {
			lines = append(lines, displaySearchPath(cwd, file))
		}
		output, note := limitSearchOutput(strings.Join(lines, "\n"), grepMaxOutput)
		return fmt.Sprintf("Found %d files\n%s", len(lines), output) + note, nil
	}
}

func formatMatch(match search.Match, cwd string, before, after int, showLine bool) ([]string, bool, error) {
	path := displaySearchPath(cwd, match.Path)
	if before == 0 && after == 0 {
		text, truncated := truncateSearchLine(match.Text)
		return []string{formatSearchLine(path, match.LineNumber, text, showLine)}, truncated, nil
	}
	data, err := os.ReadFile(match.Path)
	if err != nil {
		text, truncated := truncateSearchLine(match.Text)
		return []string{fmt.Sprintf("%s:%d: (unable to read file; %s)", path, match.LineNumber, text)}, truncated, nil
	}
	content := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	fileLines := strings.Split(content, "\n")
	start := max(1, match.LineNumber-before)
	end := min(len(fileLines), match.LineNumber+after)
	lines := make([]string, 0, end-start+1)
	truncated := false
	for lineNumber := start; lineNumber <= end; lineNumber++ {
		text, wasTruncated := truncateSearchLine(fileLines[lineNumber-1])
		truncated = truncated || wasTruncated
		if lineNumber == match.LineNumber {
			lines = append(lines, formatSearchLine(path, lineNumber, text, showLine))
		} else {
			lines = append(lines, fmt.Sprintf("%s-%d- %s", path, lineNumber, text))
		}
	}
	return lines, truncated, nil
}

func formatSearchLine(path string, line int, text string, showLine bool) string {
	if showLine {
		return fmt.Sprintf("%s:%d: %s", path, line, text)
	}
	return fmt.Sprintf("%s: %s", path, text)
}

func truncateSearchLine(text string) (string, bool) {
	text = strings.ReplaceAll(text, "\r", "")
	if len(text) <= grepMaxLineLength {
		return text, false
	}
	return text[:grepMaxLineLength], true
}

func pageBounds(length, offset, limit int) (int, int) {
	start := min(max(offset, 0), length)
	if limit == 0 {
		return start, length
	}
	return start, min(length, start+max(limit, 0))
}

func limitSearchOutput(text string, maxBytes int) (string, string) {
	if len(text) <= maxBytes {
		return text, ""
	}
	return text[:maxBytes], fmt.Sprintf("\n\n[%d byte limit reached. Use a more specific pattern or path.]", maxBytes)
}

func appendSearchNote(existing, note string) string {
	if existing == "" {
		return "\n\n[" + note + ".]"
	}
	return existing[:len(existing)-1] + "; " + note + ".]"
}

func splitGlobArguments(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, raw := range strings.Fields(value) {
		if strings.Contains(raw, "{") && strings.Contains(raw, "}") {
			out = append(out, raw)
			continue
		}
		for _, pattern := range strings.Split(raw, ",") {
			if pattern != "" {
				out = append(out, pattern)
			}
		}
	}
	return out
}

func stringArg(args map[string]any, name, fallback string) string {
	if value, ok := args[name].(string); ok && value != "" {
		return value
	}
	return fallback
}

func formatSearchError(err error) string {
	switch {
	case errors.Is(err, search.ErrTimeout):
		return "Search timed out after 20 seconds. Try a more specific path or pattern."
	case errors.Is(err, search.ErrAborted):
		return "Search aborted"
	default:
		return err.Error()
	}
}
