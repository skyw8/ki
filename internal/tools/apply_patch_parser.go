package tools

import (
	"fmt"
	"strings"
)

const (
	patchBeginMarker  = "*** Begin Patch"
	patchEndMarker    = "*** End Patch"
	patchAddMarker    = "*** Add File: "
	patchDeleteMarker = "*** Delete File: "
	patchUpdateMarker = "*** Update File: "
	patchMoveMarker   = "*** Move to: "
	patchEOFMarker    = "*** End of File"
)

type patchKind uint8

const (
	patchAdd patchKind = iota + 1
	patchDelete
	patchUpdate
)

type patchHunk struct {
	kind                    patchKind
	path, movePath, content string
	chunks                  []patchChunk
}
type patchChunk struct {
	context      string
	hasCtx       bool
	old, new     []string
	contextLines [][2]int
	eof          bool
}

func (c *patchChunk) addContext(line string) {
	c.contextLines = append(c.contextLines, [2]int{len(c.old), len(c.new)})
	c.old = append(c.old, line)
	c.new = append(c.new, line)
}

func parseApplyPatch(input string) ([]patchHunk, error) {
	input = strings.TrimSpace(input)
	lines := strings.Split(input, "\n")
	if len(lines) >= 4 && (lines[0] == "<<EOF" || lines[0] == "<<'EOF'" || lines[0] == "<<\"EOF\"") && strings.HasSuffix(lines[len(lines)-1], "EOF") {
		lines = lines[1 : len(lines)-1]
	}
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != patchBeginMarker {
		return nil, fmt.Errorf("invalid patch: The first line of the patch must be '*** Begin Patch'")
	}
	if strings.TrimSpace(strings.TrimSuffix(lines[len(lines)-1], "\r")) != patchEndMarker {
		return nil, fmt.Errorf("invalid patch: The last line of the patch must be '*** End Patch'")
	}
	p := newStreamingPatchParser()
	if _, err := p.Push(strings.Join(lines, "\n")); err != nil {
		return nil, err
	}
	return p.Finish()
}

type patchParserMode uint8

const (
	parserNotStarted patchParserMode = iota
	parserStarted
	parserAdd
	parserDelete
	parserUpdate
	parserEnded
)

type streamingPatchParser struct {
	mode       patchParserMode
	buffer     string
	line       int
	hunks      []patchHunk
	updateLine int
}

func newStreamingPatchParser() *streamingPatchParser { return &streamingPatchParser{} }

func (p *streamingPatchParser) Push(delta string) ([]patchHunk, error) {
	p.buffer += delta
	for {
		i := strings.IndexByte(p.buffer, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(p.buffer[:i], "\r")
		p.buffer = p.buffer[i+1:]
		p.line++
		if err := p.process(line); err != nil {
			return nil, err
		}
	}
	return clonePatchHunks(p.hunks), nil
}

func (p *streamingPatchParser) Finish() ([]patchHunk, error) {
	if p.buffer != "" {
		line := strings.TrimSuffix(p.buffer, "\r")
		p.buffer = ""
		p.line++
		if err := p.process(line); err != nil {
			return nil, err
		}
	}
	if p.mode != parserEnded {
		return nil, fmt.Errorf("invalid patch: The last line of the patch must be '*** End Patch'")
	}
	return clonePatchHunks(p.hunks), nil
}

func (p *streamingPatchParser) process(line string) error {
	trimmed := strings.TrimSpace(line)
	switch p.mode {
	case parserNotStarted:
		if trimmed != patchBeginMarker {
			return fmt.Errorf("invalid patch: The first line of the patch must be '*** Begin Patch'")
		}
		p.mode = parserStarted
		return nil
	case parserEnded:
		if trimmed != "" {
			return fmt.Errorf("invalid patch: The last line of the patch must be '*** End Patch'")
		}
		return nil
	case parserStarted, parserAdd, parserDelete:
		if handled, err := p.header(trimmed); handled || err != nil {
			return err
		}
		if p.mode == parserAdd && strings.HasPrefix(line, "+") {
			p.hunks[len(p.hunks)-1].content += line[1:] + "\n"
			return nil
		}
		return p.invalidHeader(trimmed)
	case parserUpdate:
		updateLine := strings.TrimRight(line, " \t\r")
		if handled, err := p.header(updateLine); handled || err != nil {
			return err
		}
		h := &p.hunks[len(p.hunks)-1]
		if len(h.chunks) > 0 && h.chunks[len(h.chunks)-1].eof {
			if updateLine == "" {
				return nil
			}
			if updateLine != "@@" && !strings.HasPrefix(updateLine, "@@ ") {
				return fmt.Errorf("invalid hunk at line %d, expected update hunk to start with a @@ context marker, got: %q", p.line, line)
			}
		}
		if len(h.chunks) == 0 && h.movePath == "" && strings.HasPrefix(updateLine, patchMoveMarker) {
			h.movePath = strings.TrimPrefix(updateLine, patchMoveMarker)
			if h.movePath == "" {
				return fmt.Errorf("invalid hunk at line %d, move path is empty", p.line)
			}
			return nil
		}
		if updateLine == "@@" || strings.HasPrefix(updateLine, "@@ ") {
			if len(h.chunks) > 0 && len(h.chunks[len(h.chunks)-1].old) == 0 && len(h.chunks[len(h.chunks)-1].new) == 0 {
				return fmt.Errorf("invalid hunk at line %d, update hunk does not contain any lines", p.line)
			}
			c := patchChunk{}
			if updateLine != "@@" {
				c.hasCtx = true
				c.context = strings.TrimPrefix(updateLine, "@@ ")
			}
			h.chunks = append(h.chunks, c)
			return nil
		}
		if updateLine == patchEOFMarker {
			if len(h.chunks) == 0 || (len(h.chunks[len(h.chunks)-1].old) == 0 && len(h.chunks[len(h.chunks)-1].new) == 0) {
				return fmt.Errorf("invalid hunk at line %d, update hunk does not contain any lines", p.line)
			}
			h.chunks[len(h.chunks)-1].eof = true
			return nil
		}
		if len(h.chunks) == 0 {
			h.chunks = append(h.chunks, patchChunk{})
		}
		c := &h.chunks[len(h.chunks)-1]
		if line == "" {
			c.addContext("")
			return nil
		}
		switch line[0] {
		case ' ':
			c.addContext(line[1:])
		case '+':
			c.new = append(c.new, line[1:])
		case '-':
			c.old = append(c.old, line[1:])
		default:
			return fmt.Errorf("invalid hunk at line %d, unexpected line found in update hunk: %q", p.line, line)
		}
		return nil
	}
	return nil
}

func (p *streamingPatchParser) header(line string) (bool, error) {
	if line == patchEndMarker {
		if err := p.ensureUpdate(); err != nil {
			return true, err
		}
		p.mode = parserEnded
		return true, nil
	}
	markers := []struct {
		prefix string
		kind   patchKind
		mode   patchParserMode
	}{
		{patchAddMarker, patchAdd, parserAdd}, {patchDeleteMarker, patchDelete, parserDelete}, {patchUpdateMarker, patchUpdate, parserUpdate},
	}
	for _, marker := range markers {
		if strings.HasPrefix(line, marker.prefix) {
			if err := p.ensureUpdate(); err != nil {
				return true, err
			}
			path := strings.TrimPrefix(line, marker.prefix)
			if path == "" {
				return true, fmt.Errorf("invalid hunk at line %d, file path is empty", p.line)
			}
			p.hunks = append(p.hunks, patchHunk{kind: marker.kind, path: path})
			p.mode = marker.mode
			if marker.kind == patchUpdate {
				p.updateLine = p.line
			}
			return true, nil
		}
	}
	return false, nil
}

func (p *streamingPatchParser) ensureUpdate() error {
	if p.mode != parserUpdate {
		return nil
	}
	h := p.hunks[len(p.hunks)-1]
	if len(h.chunks) == 0 {
		return fmt.Errorf("invalid hunk at line %d, update file hunk for path %q is empty", p.updateLine, h.path)
	}
	last := h.chunks[len(h.chunks)-1]
	if len(last.old) == 0 && len(last.new) == 0 {
		return fmt.Errorf("invalid hunk at line %d, update hunk does not contain any lines", p.line)
	}
	return nil
}

func (p *streamingPatchParser) invalidHeader(line string) error {
	return fmt.Errorf("invalid hunk at line %d, %q is not a valid hunk header", p.line, line)
}

func clonePatchHunks(in []patchHunk) []patchHunk {
	out := make([]patchHunk, len(in))
	copy(out, in)
	for i := range out {
		out[i].chunks = append([]patchChunk(nil), in[i].chunks...)
		for j := range out[i].chunks {
			out[i].chunks[j].old = append([]string(nil), in[i].chunks[j].old...)
			out[i].chunks[j].new = append([]string(nil), in[i].chunks[j].new...)
			out[i].chunks[j].contextLines = append([][2]int(nil), in[i].chunks[j].contextLines...)
		}
	}
	return out
}
