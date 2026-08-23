package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func truncateHead(s string) (out string, note string) {
	lines := strings.Split(s, "\n")
	total := len(lines)
	n := min(total, maxLines)
	chunk := strings.Join(lines[:n], "\n")
	for len(chunk) > maxBytes && n > 1 {
		n--
		chunk = strings.Join(lines[:n], "\n")
	}
	if n < total {
		return chunk, fmt.Sprintf("\n\n[Showing lines 1-%d of %d. Use offset=%d to continue.]", n, total, n+1)
	}
	if len(chunk) > maxBytes {
		return validUTF8Head(chunk, maxBytes), fmt.Sprintf("\n\n[%d byte limit reached. Use offset to continue.]", maxBytes)
	}
	return chunk, ""
}

func validUTF8Head(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

func truncateTail(s string) (out, note string) {
	lines := splitOutputLines(s)
	total := len(lines)
	if len(s) <= maxBytes && total <= maxLines {
		return s, ""
	}
	start := 0
	if total > maxLines {
		start = total - maxLines
	}
	chunk := strings.Join(lines[start:], "\n")
	for len(chunk) > maxBytes && start < total-1 {
		start++
		chunk = strings.Join(lines[start:], "\n")
	}
	if len(chunk) > maxBytes {
		// A single line can exceed the byte limit. Keep its tail without splitting
		// a UTF-8 code point so one noisy line cannot bypass the context bound.
		chunk = validUTF8Tail(chunk, maxBytes)
	}
	return chunk, fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%d byte limit).]", start+1, total, total, maxBytes)
}

// truncateTaskTail uses the bounded tail already retained by JobStore while
// reporting totals for the complete output file. Reading the whole spill file
// here would defeat the bounded-memory shell capture contract.
func truncateTaskTail(tail string, totalBytes, newlineCount int64, fullOutputPath string) (out, note string) {
	totalLines := newlineCount
	if totalBytes > 0 && !strings.HasSuffix(tail, "\n") {
		totalLines++
	}
	if totalBytes <= maxBytes && totalLines <= maxLines {
		return tail, ""
	}

	out, _ = truncateTail(tail)
	outputLines := int64(len(splitOutputLines(out)))
	startLine := max(int64(1), totalLines-outputLines+1)
	if totalLines <= 1 && totalBytes > maxBytes {
		note = fmt.Sprintf("\n\n[Showing last %d bytes of %d bytes. Full output: %s]", len(out), totalBytes, fullOutputPath)
		return out, note
	}
	if totalBytes > maxBytes {
		note = fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%d byte limit). Full output: %s]", startLine, totalLines, totalLines, maxBytes, fullOutputPath)
	} else {
		note = fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]", startLine, totalLines, totalLines, fullOutputPath)
	}
	return out, note
}

func splitOutputLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func validUTF8Tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	b := []byte(s)
	start := len(b) - limit
	for start < len(b) && !utf8.RuneStart(b[start]) {
		start++
	}
	return string(b[start:])
}

func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}
