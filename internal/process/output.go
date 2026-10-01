package process

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

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
