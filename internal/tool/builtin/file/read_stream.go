package filetools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/internal/support"
	"ki/internal/types"
)

func readFailure(err error) toolapi.Result {
	if errors.Is(err, os.ErrNotExist) {
		return support.DiagnosticError(err.Error(), "rejected", "not_found", "workspace_state")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return support.DiagnosticError("Read aborted", "cancelled", "cancelled_by_parent", "cancellation")
	}
	return support.Error(err.Error())
}

type streamUTF8 struct {
	tail  [4]byte
	count int
}

func (v *streamUTF8) valid(data []byte) bool {
	if v.count > 0 {
		for len(data) > 0 && v.count < utf8.UTFMax && !utf8.FullRune(v.tail[:v.count]) {
			v.tail[v.count] = data[0]
			v.count++
			data = data[1:]
		}
		if !utf8.FullRune(v.tail[:v.count]) {
			return true
		}
		r, n := utf8.DecodeRune(v.tail[:v.count])
		if r == utf8.RuneError && n == 1 {
			return false
		}
		v.count = 0
	}
	end := len(data)
	for back := 0; back < 3 && end > 0 && !utf8.RuneStart(data[end-1]); back++ {
		end--
	}
	if end < len(data) && end > 0 {
		end--
	}
	if end == len(data) && end > 0 && data[end-1] >= utf8.RuneSelf {
		end--
	}
	if !utf8.Valid(data[:end]) {
		return false
	}
	tail := data[end:]
	for len(tail) > 0 && utf8.FullRune(tail) {
		r, n := utf8.DecodeRune(tail)
		if r == utf8.RuneError && n == 1 {
			return false
		}
		tail = tail[n:]
	}
	v.count = copy(v.tail[:], tail)
	return true
}

type textPage struct {
	data      []byte
	lines     int
	selected  int
	startLine int
	stopped   bool
	nextLine  int
	nextByte  *int64
}

func readTextStream(ctx context.Context, source io.Reader, args map[string]any) toolapi.Result {
	off := support.IntArg(args, "offset", 1)
	if off < 1 {
		off = 1
	}
	byteOff := int64(support.IntArg(args, "byte_offset", 0))
	_, byByte := args["byte_offset"]
	if byteOff < 0 {
		return support.Error("byte_offset must be non-negative")
	}
	if byByte {
		if _, exists := args["offset"]; exists {
			return support.Error("do not combine offset and byte_offset")
		}
	}
	limit := support.IntArg(args, "limit", 0)
	if limit <= 0 {
		limit = int(^uint(0) >> 1)
	}
	reader := bufio.NewReaderSize(source, 64<<10)
	validator := streamUTF8{}
	page := textPage{data: make([]byte, 0, 1024)}
	line := 1
	var total, lineStart int64
	current := make([]byte, 0, 1024)
	currentBytes := int64(0)
	selectedLine := false
	selectionStart := int64(0)
	finishLine := func() {
		if !selectedLine || page.stopped {
			return
		}
		if page.selected >= limit || page.lines >= maxLines {
			page.stopped = true
			page.nextLine = line
			return
		}
		separator := 0
		if page.lines > 0 {
			separator = 1
		}
		remaining := maxBytes - len(page.data) - separator
		if currentBytes > int64(remaining) {
			if page.lines == 0 {
				chunk := []byte(validUTF8Head(string(current), max(remaining, 0)))
				page.data = append(page.data, chunk...)
				page.lines = 1
				page.selected = 1
				next := selectionStart + int64(len(chunk))
				page.nextByte = &next
			} else {
				page.nextLine = line
			}
			page.stopped = true
			return
		}
		if separator > 0 {
			page.data = append(page.data, '\n')
		}
		page.data = append(page.data, current...)
		page.lines++
		page.selected++
	}
	for {
		if err := ctx.Err(); err != nil {
			return readFailure(err)
		}
		if page.stopped || (!byByte && line < off) {
			chunk, peekErr := reader.Peek(64 << 10)
			if len(chunk) > 0 {
				take := len(chunk)
				count := bytes.Count(chunk, []byte{'\n'})
				if !page.stopped && count >= off-line {
					needed := off - line
					take = 0
					for needed > 0 {
						next := bytes.IndexByte(chunk[take:], '\n')
						take += next + 1
						needed--
					}
					count = off - line
				}
				if !validator.valid(chunk[:take]) {
					return support.Error("binary file; cannot read as text")
				}
				if last := bytes.LastIndexByte(chunk[:take], '\n'); last >= 0 {
					lineStart = total + int64(last) + 1
				}
				total += int64(take)
				line += count
				_, _ = reader.Discard(take)
				continue
			}
			if peekErr != nil && !errors.Is(peekErr, io.EOF) {
				return readFailure(peekErr)
			}
		}
		fragment, err := reader.ReadSlice('\n')
		if !validator.valid(fragment) {
			return support.Error("binary file; cannot read as text")
		}
		fragmentStart := total
		total += int64(len(fragment))
		hasNewline := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		body := fragment
		if hasNewline {
			body = body[:len(body)-1]
		}
		if !page.stopped {
			skip := int64(0)
			eligible := line >= off
			if byByte {
				eligible = byteOff <= fragmentStart+int64(len(body))
				if byteOff > fragmentStart {
					skip = min(int64(len(body)), byteOff-fragmentStart)
				}
				if eligible && !selectedLine && skip < int64(len(body)) && !utf8.RuneStart(body[skip]) {
					return support.Error("byte_offset must be a UTF-8 boundary")
				}
			}
			if eligible {
				if !selectedLine {
					selectedLine = true
					selectionStart = fragmentStart + skip
					if page.lines == 0 {
						page.startLine = line
					}
				}
				data := body[int(skip):]
				currentBytes += int64(len(data))
				keep := min(len(data), max(0, maxBytes+4-len(current)))
				current = append(current, data[:keep]...)
			}
		}
		if hasNewline {
			finishLine()
			line++
			lineStart = total
			current = current[:0]
			currentBytes = 0
			selectedLine = false
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
				return readFailure(err)
			}
			if errors.Is(err, io.EOF) {
				if validator.count != 0 {
					return support.Error("binary file; cannot read as text")
				}
				if !selectedLine && !page.stopped && ((byByte && byteOff <= total) || (!byByte && line >= off)) {
					selectedLine = true
					selectionStart = lineStart
				}
				finishLine()
				break
			}
		}
	}
	if byByte && byteOff > total {
		return support.Error(fmt.Sprintf("byte_offset %d is beyond end of file (%d bytes total)", byteOff, total))
	}
	if !byByte && off > line {
		return support.Error(fmt.Sprintf("Offset %d is beyond end of file (%d lines total)", off, line))
	}
	details := &truncationDetails{TotalBytes: int(total), TotalLines: line, OutputBytes: len(page.data), OutputLines: page.lines}
	note := ""
	if page.stopped {
		details.Truncated = true
		if page.nextByte != nil {
			details.Reason = "bytes"
			details.NextByteOffset = page.nextByte
			note = fmt.Sprintf("\n\n[%d byte limit reached within line %d. Use byte_offset=%d to continue.]", maxBytes, page.startLine, *page.nextByte)
		} else {
			details.NextOffset = page.nextLine
			details.Reason = "bytes_or_lines"
			if page.selected >= limit {
				details.Reason = "limit"
			}
			note = fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", page.startLine, page.startLine+page.lines-1, line, page.nextLine)
		}
	}
	return toolapi.Result{Content: []types.Content{{Type: "text", Text: string(page.data) + note}}, Details: readDetails{Truncation: details}}
}
