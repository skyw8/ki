package tools

import (
	"fmt"
	"strings"
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
		return chunk[:maxBytes], fmt.Sprintf("\n\n[%d byte limit reached. Use offset to continue.]", maxBytes)
	}
	return chunk, ""
}

func truncateTail(s string) (out, note string) {
	if len(s) <= maxBytes {
		lines := strings.Split(s, "\n")
		if len(lines) <= maxLines {
			return s, ""
		}
	}
	lines := strings.Split(s, "\n")
	total := len(lines)
	start := 0
	if total > maxLines {
		start = total - maxLines
	}
	chunk := strings.Join(lines[start:], "\n")
	for len(chunk) > maxBytes && start < total-1 {
		start++
		chunk = strings.Join(lines[start:], "\n")
	}
	return chunk, fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%d byte limit).]", start+1, total, total, maxBytes)
}

func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}
