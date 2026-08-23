package tools

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/pmezard/go-difflib/difflib"
)

type patchSourceLine struct{ text, ending string }
type patchSourceFile struct {
	lines     []patchSourceLine
	preferred string
}
type patchReplacement struct {
	start, oldN int
	lines       []patchSourceLine
}

func parsePatchSource(contents string) patchSourceFile {
	f := patchSourceFile{preferred: "\n"}
	preferred, start := "", 0
	for i := 0; i < len(contents); {
		ending, n := "", 0
		switch contents[i] {
		case '\r':
			if i+1 < len(contents) && contents[i+1] == '\n' {
				ending, n = "\r\n", 2
			} else {
				ending, n = "\r", 1
			}
		case '\n':
			ending, n = "\n", 1
		}
		if n == 0 {
			i++
			continue
		}
		if preferred == "" {
			preferred = ending
		}
		f.lines = append(f.lines, patchSourceLine{text: contents[start:i], ending: ending})
		i += n
		start = i
	}
	if start < len(contents) {
		f.lines = append(f.lines, patchSourceLine{text: contents[start:]})
	}
	if preferred != "" {
		f.preferred = preferred
	}
	return f
}

func (f patchSourceFile) texts() []string {
	out := make([]string, len(f.lines))
	for i := range f.lines {
		out[i] = f.lines[i].text
	}
	return out
}
func (f *patchSourceFile) apply(repls []patchReplacement) {
	sort.Slice(repls, func(i, j int) bool { return repls[i].start > repls[j].start })
	for _, r := range repls {
		next := make([]patchSourceLine, 0, len(f.lines)-r.oldN+len(r.lines))
		next = append(next, f.lines[:r.start]...)
		next = append(next, r.lines...)
		next = append(next, f.lines[r.start+r.oldN:]...)
		f.lines = next
	}
	for i := range f.lines {
		if f.lines[i].ending == "" {
			f.lines[i].ending = f.preferred
		}
	}
}
func (f patchSourceFile) contents() string {
	var b strings.Builder
	for _, line := range f.lines {
		b.WriteString(line.text)
		b.WriteString(line.ending)
	}
	return b.String()
}

func derivePatchUpdate(contents, path string, chunks []patchChunk) (string, string, error) {
	source := parsePatchSource(contents)
	texts := source.texts()
	cursor := 0
	var repls []patchReplacement
	for _, chunk := range chunks {
		if chunk.hasCtx {
			idx := seekPatchSequence(texts, []string{chunk.context}, cursor, false)
			if idx < 0 {
				return "", "", fmt.Errorf("%w %q in %s", errPatchContextMissing, chunk.context, path)
			}
			cursor = idx + 1
		}
		if len(chunk.old) == 0 {
			repls = append(repls, patchReplacement{start: len(texts), lines: insertedPatchLines(chunk.new, source.preferred)})
			continue
		}
		pattern := append([]string(nil), chunk.old...)
		replacement := append([]string(nil), chunk.new...)
		idx := seekPatchSequence(texts, pattern, cursor, chunk.eof)
		if idx < 0 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(replacement) > 0 && replacement[len(replacement)-1] == "" {
				replacement = replacement[:len(replacement)-1]
			}
			idx = seekPatchSequence(texts, pattern, cursor, chunk.eof)
		}
		if idx < 0 {
			return "", "", fmt.Errorf("%w in %s:\n%s", errPatchLinesMissing, path, strings.Join(chunk.old, "\n"))
		}
		newLines := insertedPatchLines(replacement, source.preferred)
		// Context lines are semantically unchanged. Reuse their source records so
		// mixed line endings do not turn a small edit into a whole-file diff.
		for _, pair := range chunk.contextLines {
			oldIdx, newIdx := pair[0], pair[1]
			if oldIdx < len(pattern) && newIdx < len(newLines) && idx+oldIdx < len(source.lines) {
				newLines[newIdx] = source.lines[idx+oldIdx]
			}
		}
		repls = append(repls, patchReplacement{start: idx, oldN: len(pattern), lines: newLines})
		cursor = idx + len(pattern)
	}
	source.apply(repls)
	next := source.contents()
	diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(contents), B: difflib.SplitLines(next), FromFile: path, ToFile: path, Context: 1})
	return next, diff, nil
}

func insertedPatchLines(lines []string, ending string) []patchSourceLine {
	out := make([]patchSourceLine, len(lines))
	for i, line := range lines {
		out[i] = patchSourceLine{text: line, ending: ending}
	}
	return out
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
	for mode := range 4 {
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
