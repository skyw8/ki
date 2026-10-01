package filetools

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
