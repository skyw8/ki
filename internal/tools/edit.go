package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"ki/internal/loop"
	"ki/internal/types"
)

type editTool struct {
	cwd       string
	mutations *MutationQueue
}

type editInput struct{ Old, New string }
type editMatch struct {
	start, end int
	newText    string
}
type editDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine int    `json:"first_changed_line,omitempty"`
}

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
- Use old_string/new_string for one replacement, or edits[] for multiple disjoint replacements based on the same original file. Do not combine the two modes.
- Each edits[].old_string must be unique and must not overlap another edit. Merge nearby or overlapping changes into one entry.
- The single-edit form will FAIL if old_string is not unique. Provide more context or use replace_all to change every instance.
- Use replace_all for replacing and renaming strings across the file. This parameter is useful if you want to rename a variable for instance.`
}
func (editTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"file_path"},
		"properties": map[string]any{
			"file_path":   map[string]any{"type": "string", "description": "The absolute path to the file to modify"},
			"old_string":  map[string]any{"type": "string", "description": "The text to replace"},
			"new_string":  map[string]any{"type": "string", "description": "The text to replace it with (must be different from old_string)"},
			"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences of old_string (default false)"},
			"edits": map[string]any{"type": "array", "minItems": 1, "description": "Multiple non-overlapping replacements, all matched against the original file. Cannot be combined with old_string/new_string/replace_all.", "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []any{"old_string", "new_string"}, "properties": map[string]any{
					"old_string": map[string]any{"type": "string"}, "new_string": map[string]any{"type": "string"},
				},
			}},
		},
	}
}

func (t editTool) Validate(args map[string]any) error {
	if err := validateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	_, hasEdits := args["edits"]
	_, hasOld := args["old_string"]
	_, hasNew := args["new_string"]
	_, hasAll := args["replace_all"]
	if hasEdits && (hasOld || hasNew || hasAll) {
		return fmt.Errorf("%w: edits cannot be combined with old_string, new_string, or replace_all", errToolExecution)
	}
	if !hasEdits && (!hasOld || !hasNew) {
		return fmt.Errorf("%w: provide old_string/new_string or edits", errToolExecution)
	}
	return nil
}

func (t editTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	path, _ := args["file_path"].(string)
	if path == "" {
		return errRes("file_path is required")
	}
	edits, batch, err := parseEditInputs(args)
	if err != nil {
		return errRes(err.Error())
	}
	abs := resolve(t.cwd, path)
	queue := t.mutations
	if queue == nil {
		queue = NewMutationQueue()
	}
	release, err := queue.LockPaths(ctx, abs)
	if err != nil {
		return errRes("Edit aborted")
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return errRes("Edit aborted")
	}
	//nolint:gosec // abs is normalized and confined to the requested tool path.
	data, err := os.ReadFile(abs)
	if err != nil {
		return errRes(fmt.Sprintf("Could not edit file: %s. %s", path, err.Error()))
	}
	if err := ctx.Err(); err != nil {
		return errRes("Edit aborted")
	}
	text := string(data)
	matches, err := matchEdits(text, edits, batch, boolArg(args, "replace_all"), path)
	if err != nil {
		return errRes(err.Error())
	}
	next := applyEditMatches(text, matches)
	// resolve validates the tool path against the session working directory.

	if err := os.WriteFile(abs, []byte(next), 0o600); err != nil {
		return errRes(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return errRes("Edit aborted")
	}
	details := makeEditDetails(path, text, next, matches[0].start)
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(matches), path)}}, Details: details}
}

func parseEditInputs(args map[string]any) ([]editInput, bool, error) {
	rawValue, hasEdits := args["edits"]
	if hasEdits {
		raw, ok := rawValue.([]any)
		if !ok {
			return nil, true, errEditsArray
		}
		out := make([]editInput, 0, len(raw))
		for i, value := range raw {
			item, ok := value.(map[string]any)
			if !ok {
				return nil, true, fmt.Errorf("edits[%d] %w", i, errEditObject)
			}
			oldS, _ := item["old_string"].(string)
			newS, _ := item["new_string"].(string)
			if oldS == "" {
				return nil, true, fmt.Errorf("edits[%d].%w", i, errEditOldEmpty)
			}
			if oldS == newS {
				return nil, true, fmt.Errorf("edits[%d] %w", i, errEditNoChange)
			}
			out = append(out, editInput{Old: oldS, New: newS})
		}
		if len(out) == 0 {
			return nil, true, errEditsEmpty
		}
		return out, true, nil
	}
	oldS, _ := args["old_string"].(string)
	newS, _ := args["new_string"].(string)
	if oldS == "" {
		return nil, false, errOldStringRequired
	}
	if oldS == newS {
		return nil, false, errNoChanges
	}
	return []editInput{{Old: oldS, New: newS}}, false, nil
}

func boolArg(args map[string]any, key string) bool { v, _ := args[key].(bool); return v }

func matchEdits(text string, edits []editInput, batch, replaceAll bool, path string) ([]editMatch, error) {
	var matches []editMatch
	for i, edit := range edits {
		count := strings.Count(text, edit.Old)
		if count == 0 {
			return nil, fmt.Errorf("%w for edit %d in %s; it must match exactly including whitespace and newlines", errCouldNotFindOldString, i, path)
		}
		if batch && count != 1 {
			return nil, fmt.Errorf("%w %d occurrences for edits[%d].old_string in %s; each batch old_string must be unique", errFoundOccurrences, count, i, path)
		}
		if !batch && count > 1 && !replaceAll {
			return nil, fmt.Errorf("%w %d occurrences of old_string in %s; use replace_all or provide more context", errFoundOccurrences, count, path)
		}
		from := 0
		for {
			at := strings.Index(text[from:], edit.Old)
			if at < 0 {
				break
			}
			at += from
			matches = append(matches, editMatch{start: at, end: at + len(edit.Old), newText: edit.New})
			if !replaceAll || batch {
				break
			}
			from = at + len(edit.Old)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	for i := 1; i < len(matches); i++ {
		if matches[i].start < matches[i-1].end {
			return nil, fmt.Errorf("%w in %s; merge the overlapping replacements", errEditsOverlap, path)
		}
	}
	return matches, nil
}

func applyEditMatches(text string, matches []editMatch) string {
	var out strings.Builder
	cursor := 0
	for _, match := range matches {
		out.WriteString(text[cursor:match.start])
		out.WriteString(match.newText)
		cursor = match.end
	}
	out.WriteString(text[cursor:])
	return out.String()
}

func makeEditDetails(path, before, after string, first int) editDetails {
	patch, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(before), B: difflib.SplitLines(after), FromFile: path, ToFile: path, Context: 3})
	return editDetails{Diff: patch, Patch: patch, FirstChangedLine: strings.Count(before[:first], "\n") + 1}
}
