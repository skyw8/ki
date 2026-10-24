package filetools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func truncateHead(s string) (out, note string) {
	total := strings.Count(s, "\n") + 1
	end, n := 0, 0
	for n < total && n < maxLines {
		next := len(s)
		if i := strings.IndexByte(s[end:], '\n'); i >= 0 {
			next = end + i
		}
		if next > maxBytes {
			if n == 0 {
				return validUTF8Head(s, maxBytes), fmt.Sprintf("\n\n[%d byte limit reached. Use offset to continue.]", maxBytes)
			}
			break
		}
		n++
		if next == len(s) {
			end = next
			break
		}
		end = next + 1
	}
	if n < total {
		if end > 0 && s[end-1] == '\n' {
			end--
		}
		return s[:end], fmt.Sprintf("\n\n[Showing lines 1-%d of %d. Use offset=%d to continue.]", n, total, n+1)
	}
	return s[:end], ""
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
