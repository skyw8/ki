package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"ki/internal/loop"
	"ki/internal/types"
)

const applyPatchGrammar = `start: begin_patch hunk+ end_patch
begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?

hunk: add_hunk | delete_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
delete_hunk: "*** Delete File: " filename LF
update_hunk: "*** Update File: " filename LF change_move? change?
filename: /(.+)/
add_line: "+" /(.*)/ LF -> line
change_move: "*** Move to: " filename LF
change: (change_context | change_line)+ eof_line?
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF
%import common.LF`

type applyPatchTool struct {
	cwd       string
	mutations *MutationQueue
	io        *patchFileIO
}

func (applyPatchTool) Name() string { return "apply_patch" }
func (applyPatchTool) Description() string {
	return "The `apply_patch` tool can be used to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON."
}
func (applyPatchTool) Prompt() string             { return "" }
func (applyPatchTool) Snippet() string            { return "Apply a patch to files" }
func (applyPatchTool) Parameters() map[string]any { return nil }
func (applyPatchTool) Execute(context.Context, map[string]any) loop.ToolResult {
	return errRes("apply_patch requires freeform input")
}
func (t applyPatchTool) ToolSpec() loop.ToolSpec {
	return loop.ToolSpec{Type: "custom", Name: t.Name(), Description: t.Description(), Format: &loop.ToolFormat{Type: "grammar", Syntax: "lark", Definition: applyPatchGrammar}}
}

type patchFileIO struct {
	readFile  func(string) ([]byte, error)
	writeFile func(string, []byte, fs.FileMode) error
	mkdirAll  func(string, fs.FileMode) error
	remove    func(string) error
	stat      func(string) (fs.FileInfo, error)
	lstat     func(string) (fs.FileInfo, error)
}

var localPatchIO = patchFileIO{readFile: os.ReadFile, writeFile: os.WriteFile, mkdirAll: os.MkdirAll, remove: os.Remove, stat: os.Stat, lstat: os.Lstat}

type verifiedPatch struct {
	changes []verifiedPatchChange
}
type verifiedPatchChange struct {
	hunk                                               patchHunk
	kind                                               patchKind
	path, display, movePath, content, oldContent, diff string
}

type appliedPatchDelta struct {
	changes []appliedPatchChange
	exact   bool
}
type appliedPatchChange struct {
	kind                                            patchKind
	path, display, movePath, oldContent, newContent string
	overwrittenContent, overwrittenMoveContent      *string
}

type applyPatchDetails struct {
	Status  string                   `json:"status"`
	Exact   bool                     `json:"exact"`
	Changes []applyPatchChangeDetail `json:"changes"`
}
type applyPatchChangeDetail struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	MovePath    string `json:"move_path,omitempty"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}

func (t applyPatchTool) ExecuteRaw(ctx context.Context, input string) loop.ToolResult {
	hunks, err := parseApplyPatch(input)
	if err != nil {
		return patchResult("apply_patch verification failed: "+err.Error(), true, appliedPatchDelta{exact: true})
	}
	queue := t.mutations
	if queue == nil {
		queue = NewMutationQueue()
	}
	release, err := queue.LockPaths(ctx, patchPaths(t.cwd, hunks)...)
	if err != nil {
		return patchResult("apply_patch aborted", true, appliedPatchDelta{exact: true})
	}
	defer release()
	pio := t.io
	if pio == nil {
		pio = &localPatchIO
	}
	// Verify every deterministic read and context match before the first write;
	// otherwise a later bad hunk would leave an avoidable committed prefix.
	verified, err := verifyApplyPatch(ctx, t.cwd, hunks, pio)
	if err != nil {
		return patchResult("apply_patch verification failed: "+err.Error(), true, appliedPatchDelta{exact: true})
	}
	summary, delta, err := applyVerifiedPatch(ctx, verified, pio)
	if err != nil {
		return patchResult(err.Error(), true, delta)
	}
	return patchResult(summary, false, delta)
}

func patchPaths(cwd string, hunks []patchHunk) []string {
	paths := make([]string, 0, len(hunks)*2)
	for _, h := range hunks {
		paths = append(paths, resolve(cwd, filepath.FromSlash(h.path)))
		if h.movePath != "" {
			paths = append(paths, resolve(cwd, filepath.FromSlash(h.movePath)))
		}
	}
	return paths
}

func verifyApplyPatch(ctx context.Context, cwd string, hunks []patchHunk, pio *patchFileIO) (verifiedPatch, error) {
	if len(hunks) == 0 {
		return verifiedPatch{}, fmt.Errorf("No files were modified.")
	}
	verified := verifiedPatch{changes: make([]verifiedPatchChange, 0, len(hunks))}
	seen := map[string]bool{}
	for _, h := range hunks {
		if err := ctx.Err(); err != nil {
			return verifiedPatch{}, fmt.Errorf("apply_patch aborted: %w", err)
		}
		path := resolve(cwd, filepath.FromSlash(h.path))
		key := canonicalMutationPath(path)
		if seen[key] {
			return verifiedPatch{}, fmt.Errorf("invalid patch: multiple operations target %s", path)
		}
		seen[key] = true
		change := verifiedPatchChange{hunk: h, kind: h.kind, path: path, display: h.path}
		if h.movePath != "" {
			change.movePath = resolve(cwd, filepath.FromSlash(h.movePath))
		}
		switch h.kind {
		case patchAdd:
			change.content = h.content
		case patchDelete:
			data, err := pio.readFile(path)
			if err != nil {
				return verifiedPatch{}, fmt.Errorf("Failed to read %s: %w", path, err)
			}
			change.oldContent = string(data)
		case patchUpdate:
			data, err := pio.readFile(path)
			if err != nil {
				return verifiedPatch{}, fmt.Errorf("Failed to read file to update %s: %w", path, err)
			}
			next, diff, err := derivePatchUpdate(string(data), path, h.chunks)
			if err != nil {
				return verifiedPatch{}, err
			}
			change.oldContent, change.content, change.diff = string(data), next, diff
		}
		verified.changes = append(verified.changes, change)
	}
	return verified, nil
}

func applyVerifiedPatch(ctx context.Context, verified verifiedPatch, pio *patchFileIO) (string, appliedPatchDelta, error) {
	delta := appliedPatchDelta{exact: true}
	var added, modified, deleted []string
	for _, intended := range verified.changes {
		h := intended.hunk
		if err := ctx.Err(); err != nil {
			return "", delta, fmt.Errorf("apply_patch aborted: %w", err)
		}
		path := intended.path
		switch intended.kind {
		case patchAdd:
			overwritten := optionalPatchContent(path, pio, &delta.exact)
			if err := writePatchFile(path, []byte(intended.content), pio); err != nil {
				// WriteFile may truncate before returning ENOSPC or another error,
				// so the observed failure cannot prove the target is unchanged.
				delta.exact = false
				return "", delta, fmt.Errorf("Failed to write file %s: %w", path, err)
			}
			delta.changes = append(delta.changes, appliedPatchChange{kind: patchAdd, path: path, display: intended.display, newContent: intended.content, overwrittenContent: overwritten})
			added = append(added, intended.display)
		case patchDelete:
			notePatchPath(path, pio, &delta.exact)
			old, readErr := pio.readFile(path)
			if readErr != nil {
				delta.exact = false
			}
			st, err := pio.stat(path)
			if err != nil {
				return "", delta, fmt.Errorf("Failed to delete file %s: %w", path, err)
			}
			if st.IsDir() {
				return "", delta, fmt.Errorf("Failed to delete file %s: path is a directory", path)
			}
			if err := pio.remove(path); err != nil {
				delta.exact = delta.exact && patchContentUnchanged(path, old, readErr == nil, pio)
				return "", delta, fmt.Errorf("Failed to delete file %s: %w", path, err)
			}
			if readErr == nil {
				delta.changes = append(delta.changes, appliedPatchChange{kind: patchDelete, path: path, display: h.path, oldContent: string(old)})
			}
			deleted = append(deleted, h.path)
		case patchUpdate:
			notePatchPath(path, pio, &delta.exact)
			data, err := pio.readFile(path)
			if err != nil {
				return "", delta, fmt.Errorf("Failed to read file to update %s: %w", path, err)
			}
			next, _, err := derivePatchUpdate(string(data), path, h.chunks)
			if err != nil {
				return "", delta, err
			}
			if h.movePath == "" {
				if err := writePatchFile(path, []byte(next), pio); err != nil {
					delta.exact = false
					return "", delta, fmt.Errorf("Failed to write file %s: %w", path, err)
				}
				delta.changes = append(delta.changes, appliedPatchChange{kind: patchUpdate, path: path, display: h.path, oldContent: string(data), newContent: next})
				modified = append(modified, intended.display)
			} else {
				dest := intended.movePath
				overwritten := optionalPatchContent(dest, pio, &delta.exact)
				if err := writePatchFile(dest, []byte(next), pio); err != nil {
					delta.exact = false
					return "", delta, fmt.Errorf("Failed to write file %s: %w", dest, err)
				}
				idx := len(delta.changes)
				delta.changes = append(delta.changes, appliedPatchChange{kind: patchAdd, path: dest, display: h.movePath, newContent: next, overwrittenContent: overwritten})
				if err := pio.remove(path); err != nil {
					delta.exact = delta.exact && patchContentUnchanged(path, data, true, pio)
					return "", delta, fmt.Errorf("Failed to remove original %s: %w", path, err)
				}
				delta.changes[idx] = appliedPatchChange{kind: patchUpdate, path: path, display: h.path, movePath: dest, oldContent: string(data), newContent: next, overwrittenMoveContent: overwritten}
				modified = append(modified, h.movePath)
			}
		}
		if err := ctx.Err(); err != nil {
			return "", delta, fmt.Errorf("apply_patch aborted: %w", err)
		}
	}
	var out strings.Builder
	out.WriteString("Success. Updated the following files:\n")
	for _, path := range added {
		fmt.Fprintf(&out, "A %s\n", path)
	}
	for _, path := range modified {
		fmt.Fprintf(&out, "M %s\n", path)
	}
	for _, path := range deleted {
		fmt.Fprintf(&out, "D %s\n", path)
	}
	return out.String(), delta, nil
}

func writePatchFile(path string, content []byte, pio *patchFileIO) error {
	err := pio.writeFile(path, content, 0o600)
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := pio.mkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return pio.writeFile(path, content, 0o600)
}
func notePatchPath(path string, pio *patchFileIO, exact *bool) {
	st, err := pio.lstat(path)
	if err == nil && st.Mode().IsRegular() {
		return
	}
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	*exact = false
}
func optionalPatchContent(path string, pio *patchFileIO, exact *bool) *string {
	notePatchPath(path, pio, exact)
	b, err := pio.readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		*exact = false
		return nil
	}
	s := string(b)
	return &s
}
func patchContentUnchanged(path string, expected []byte, valid bool, pio *patchFileIO) bool {
	if !valid {
		return false
	}
	got, err := pio.readFile(path)
	return err == nil && string(got) == string(expected)
}

func patchResult(text string, failed bool, delta appliedPatchDelta) loop.ToolResult {
	status := "completed"
	if failed {
		status = "failed"
	}
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: text}}, IsError: failed, Details: applyPatchDetails{Status: status, Exact: delta.exact, Changes: patchDeltaDetails(delta)}}
}
func patchDeltaDetails(delta appliedPatchDelta) []applyPatchChangeDetail {
	out := make([]applyPatchChangeDetail, 0, len(delta.changes))
	for _, change := range delta.changes {
		from, to := change.path, change.path
		oldContent := change.oldContent
		if change.kind == patchAdd && change.overwrittenContent != nil {
			oldContent = *change.overwrittenContent
		}
		if change.kind == patchAdd && change.overwrittenContent == nil {
			from = change.path + " (new)"
		}
		if change.kind == patchDelete {
			to = change.path + " (deleted)"
		}
		if change.movePath != "" {
			to = change.movePath
		}
		diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(oldContent), B: difflib.SplitLines(change.newContent), FromFile: from, ToFile: to, Context: 1})
		out = append(out, applyPatchChangeDetail{Path: change.path, Kind: patchKindName(change.kind), MovePath: change.movePath, UnifiedDiff: diff})
	}
	return out
}
func patchKindName(kind patchKind) string {
	switch kind {
	case patchAdd:
		return "add"
	case patchDelete:
		return "delete"
	default:
		return "update"
	}
}
