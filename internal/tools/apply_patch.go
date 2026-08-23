package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"ki/internal/loop"
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
	return loop.ToolSpec{
		Type:        "custom",
		Name:        t.Name(),
		Description: t.Description(),
		Format: &loop.ToolFormat{
			Type:       "grammar",
			Syntax:     "lark",
			Definition: applyPatchGrammar,
		},
	}
}
func (t applyPatchTool) ExecuteRaw(ctx context.Context, input string) loop.ToolResult {
	hunks, err := parseApplyPatch(input)
	if err != nil {
		return errRes("apply_patch verification failed: " + err.Error())
	}
	paths := make([]string, 0, len(hunks)*2)
	for _, h := range hunks {
		paths = append(paths, resolve(t.cwd, filepath.FromSlash(h.path)))
		if h.movePath != "" {
			paths = append(paths, resolve(t.cwd, filepath.FromSlash(h.movePath)))
		}
	}
	queue := t.mutations
	if queue == nil {
		queue = NewMutationQueue()
	}
	release, err := queue.LockPaths(ctx, paths...)
	if err != nil {
		return errRes("apply_patch aborted")
	}
	defer release()
	summary, err := applyPatchHunks(ctx, t.cwd, hunks)
	if err != nil {
		return errRes(err.Error())
	}
	return txt(summary)
}

type patchKind uint8

const (
	patchAdd patchKind = iota + 1
	patchDelete
	patchUpdate
)

type patchHunk struct {
	kind     patchKind
	path     string
	movePath string
	content  string
	chunks   []patchChunk
}

type patchChunk struct {
	context string
	hasCtx  bool
	old     []string
	new     []string
	eof     bool
}

func parseApplyPatch(input string) ([]patchHunk, error) {
	input = strings.TrimSpace(input)
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	// Codex accepts the common heredoc wrapper in lenient mode.
	if len(lines) >= 4 && (lines[0] == "<<EOF" || lines[0] == "<<'EOF'" || lines[0] == "<<\"EOF\"") && strings.HasSuffix(lines[len(lines)-1], "EOF") {
		lines = lines[1 : len(lines)-1]
	}
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return nil, fmt.Errorf("invalid patch: The first line of the patch must be '*** Begin Patch'")
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return nil, fmt.Errorf("invalid patch: The last line of the patch must be '*** End Patch'")
	}

	var hunks []patchHunk
	for i := 1; i < len(lines)-1; {
		lineNo := i + 1
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimPrefix(line, "*** Add File: ")
			if path == "" {
				return nil, fmt.Errorf("invalid hunk at line %d, file path is empty", lineNo)
			}
			i++
			var content strings.Builder
			count := 0
			for i < len(lines)-1 && !strings.HasPrefix(strings.TrimSpace(lines[i]), "*** ") {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("invalid hunk at line %d, add-file lines must start with '+'", i+1)
				}
				content.WriteString(strings.TrimPrefix(lines[i], "+"))
				content.WriteByte('\n')
				count++
				i++
			}
			if count == 0 {
				return nil, fmt.Errorf("invalid hunk at line %d, add-file hunk is empty", lineNo)
			}
			hunks = append(hunks, patchHunk{kind: patchAdd, path: path, content: content.String()})

		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimPrefix(line, "*** Delete File: ")
			if path == "" {
				return nil, fmt.Errorf("invalid hunk at line %d, file path is empty", lineNo)
			}
			hunks = append(hunks, patchHunk{kind: patchDelete, path: path})
			i++

		case strings.HasPrefix(line, "*** Update File: "):
			h := patchHunk{kind: patchUpdate, path: strings.TrimPrefix(line, "*** Update File: ")}
			if h.path == "" {
				return nil, fmt.Errorf("invalid hunk at line %d, file path is empty", lineNo)
			}
			i++
			if i < len(lines)-1 && strings.HasPrefix(strings.TrimSpace(lines[i]), "*** Move to: ") {
				h.movePath = strings.TrimPrefix(strings.TrimSpace(lines[i]), "*** Move to: ")
				i++
			}
			for i < len(lines)-1 && !isPatchFileHeader(strings.TrimSpace(lines[i])) {
				chunk := patchChunk{}
				marker := strings.TrimSpace(lines[i])
				if marker == "@@" {
					i++
				} else if strings.HasPrefix(marker, "@@ ") {
					chunk.context = strings.TrimPrefix(marker, "@@ ")
					chunk.hasCtx = true
					i++
				} else if len(h.chunks) > 0 {
					return nil, fmt.Errorf("invalid hunk at line %d, expected '@@' context marker", i+1)
				}

				for i < len(lines)-1 {
					cur := lines[i]
					trimmed := strings.TrimSpace(cur)
					if isPatchFileHeader(trimmed) || trimmed == "@@" || strings.HasPrefix(trimmed, "@@ ") {
						break
					}
					if trimmed == "*** End of File" {
						chunk.eof = true
						i++
						break
					}
					if cur == "" {
						chunk.old = append(chunk.old, "")
						chunk.new = append(chunk.new, "")
						i++
						continue
					}
					switch cur[0] {
					case ' ':
						chunk.old = append(chunk.old, cur[1:])
						chunk.new = append(chunk.new, cur[1:])
					case '+':
						chunk.new = append(chunk.new, cur[1:])
					case '-':
						chunk.old = append(chunk.old, cur[1:])
					default:
						return nil, fmt.Errorf("invalid hunk at line %d, every line must start with ' ', '+' or '-'", i+1)
					}
					i++
				}
				if len(chunk.old) == 0 && len(chunk.new) == 0 {
					return nil, fmt.Errorf("invalid hunk at line %d, update hunk does not contain any lines", lineNo)
				}
				h.chunks = append(h.chunks, chunk)
			}
			if len(h.chunks) == 0 {
				return nil, fmt.Errorf("invalid hunk at line %d, update file hunk for path %q is empty", lineNo, h.path)
			}
			hunks = append(hunks, h)

		default:
			return nil, fmt.Errorf("invalid hunk at line %d, %q is not a valid hunk header", lineNo, line)
		}
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("No files were modified.")
	}
	return hunks, nil
}

func isPatchFileHeader(line string) bool {
	return line == "*** End Patch" || strings.HasPrefix(line, "*** Add File: ") || strings.HasPrefix(line, "*** Delete File: ") || strings.HasPrefix(line, "*** Update File: ")
}

type patchReplacement struct {
	start int
	oldN  int
	new   []string
}

func applyPatchHunks(ctx context.Context, cwd string, hunks []patchHunk) (string, error) {
	var added, modified, deleted []string
	for _, h := range hunks {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("apply_patch aborted: %w", err)
		}
		path := resolve(cwd, filepath.FromSlash(h.path))
		switch h.kind {
		case patchAdd:
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return "", fmt.Errorf("Failed to create parent directory for %s: %w", path, err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("apply_patch aborted: %w", err)
			}
			if err := os.WriteFile(path, []byte(h.content), 0o600); err != nil {
				return "", fmt.Errorf("Failed to write file %s: %w", path, err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("apply_patch aborted: %w", err)
			}
			added = append(added, h.path)

		case patchDelete:
			st, err := os.Stat(path)
			if err != nil {
				return "", fmt.Errorf("Failed to delete file %s: %w", path, err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("apply_patch aborted: %w", err)
			}
			if st.IsDir() {
				return "", fmt.Errorf("Failed to delete file %s: path is a directory", path)
			}
			if err := os.Remove(path); err != nil {
				return "", fmt.Errorf("Failed to delete file %s: %w", path, err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("apply_patch aborted: %w", err)
			}
			deleted = append(deleted, h.path)

		case patchUpdate:
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("Failed to read file to update %s: %w", path, err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("apply_patch aborted: %w", err)
			}
			next, err := applyUpdateChunks(string(data), path, h.chunks)
			if err != nil {
				return "", err
			}
			dest := path
			if h.movePath != "" {
				dest = resolve(cwd, filepath.FromSlash(h.movePath))
				if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
					return "", fmt.Errorf("Failed to create parent directory for %s: %w", dest, err)
				}
				if err := ctx.Err(); err != nil {
					return "", fmt.Errorf("apply_patch aborted: %w", err)
				}
			}
			if err := os.WriteFile(dest, []byte(next), 0o600); err != nil {
				return "", fmt.Errorf("Failed to write file %s: %w", dest, err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("apply_patch aborted: %w", err)
			}
			if dest != path {
				if err := os.Remove(path); err != nil {
					return "", fmt.Errorf("Failed to remove original %s: %w", path, err)
				}
				if err := ctx.Err(); err != nil {
					return "", fmt.Errorf("apply_patch aborted: %w", err)
				}
			}
			modified = append(modified, map[bool]string{true: h.movePath, false: h.path}[h.movePath != ""])
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
	return out.String(), nil
}

func applyUpdateChunks(contents, path string, chunks []patchChunk) (string, error) {
	lines := strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	cursor := 0
	var replacements []patchReplacement
	for _, chunk := range chunks {
		if chunk.hasCtx {
			idx := seekPatchSequence(lines, []string{chunk.context}, cursor, false)
			if idx < 0 {
				return "", fmt.Errorf("Failed to find context %q in %s", chunk.context, path)
			}
			cursor = idx + 1
		}
		if len(chunk.old) == 0 {
			replacements = append(replacements, patchReplacement{start: len(lines), new: chunk.new})
			continue
		}
		pattern, replacement := chunk.old, chunk.new
		idx := seekPatchSequence(lines, pattern, cursor, chunk.eof)
		if idx < 0 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(replacement) > 0 && replacement[len(replacement)-1] == "" {
				replacement = replacement[:len(replacement)-1]
			}
			idx = seekPatchSequence(lines, pattern, cursor, chunk.eof)
		}
		if idx < 0 {
			return "", fmt.Errorf("Failed to find expected lines in %s:\n%s", path, strings.Join(chunk.old, "\n"))
		}
		replacements = append(replacements, patchReplacement{start: idx, oldN: len(pattern), new: append([]string(nil), replacement...)})
		cursor = idx + len(pattern)
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start > replacements[j].start })
	for _, r := range replacements {
		next := make([]string, 0, len(lines)-r.oldN+len(r.new))
		next = append(next, lines[:r.start]...)
		next = append(next, r.new...)
		next = append(next, lines[r.start+r.oldN:]...)
		lines = next
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func seekPatchSequence(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return start
	}
	if len(pattern) > len(lines) {
		return -1
	}
	search := start
	if eof {
		search = len(lines) - len(pattern)
	}
	for mode := 0; mode < 4; mode++ {
		for i := search; i <= len(lines)-len(pattern); i++ {
			ok := true
			for j := range pattern {
				if normalizePatchLine(lines[i+j], mode) != normalizePatchLine(pattern[j], mode) {
					ok = false
					break
				}
			}
			if ok {
				return i
			}
		}
	}
	if eof && search != start {
		return seekPatchSequence(lines, pattern, start, false)
	}
	return -1
}

func normalizePatchLine(s string, mode int) string {
	switch mode {
	case 1:
		return strings.TrimRightFunc(s, unicode.IsSpace)
	case 2:
		return strings.TrimSpace(s)
	case 3:
		var b strings.Builder
		for _, r := range strings.TrimSpace(s) {
			switch r {
			case '‐', '‑', '‒', '–', '—', '―', '−':
				r = '-'
			case '‘', '’', '‚', '‛':
				r = '\''
			case '“', '”', '„', '‟':
				r = '"'
			case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
				r = ' '
			}
			b.WriteRune(r)
		}
		return b.String()
	default:
		return s
	}
}
