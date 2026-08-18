package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func formatNotebook(data []byte) string {
	var nb struct {
		Cells []struct {
			CellType string `json:"cell_type"`
			Source   any    `json:"source"`
			Outputs  []struct {
				Text any `json:"text"`
			} `json:"outputs"`
		} `json:"cells"`
	}
	if json.Unmarshal(data, &nb) != nil {
		return string(data)
	}
	var b strings.Builder
	for i, c := range nb.Cells {
		fmt.Fprintf(&b, "=== cell %d (%s) ===\n", i, c.CellType)
		b.WriteString(joinSrc(c.Source))
		b.WriteByte('\n')
		for _, o := range c.Outputs {
			if s := joinSrc(o.Text); s != "" {
				b.WriteString(s)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func joinSrc(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var s strings.Builder
		for _, x := range t {
			_, _ = fmt.Fprint(&s, x)
		}
		return s.String()
	}
	return ""
}

func extractPDFText(data []byte, pages string) string {
	// Best-effort: pull printable strings from PDF streams.
	var b strings.Builder
	if pages != "" {
		fmt.Fprintf(&b, "[pages=%s]\n", pages)
	}
	inParen := false
	var cur []byte
	for i := range data {
		c := data[i]
		if c == '(' {
			inParen = true
			cur = cur[:0]
			continue
		}
		if inParen && c == ')' {
			inParen = false
			if len(cur) > 0 && utf8.Valid(cur) {
				b.Write(cur)
				b.WriteByte('\n')
			}
			continue
		}
		if inParen {
			cur = append(cur, c)
		}
	}
	if b.Len() == 0 {
		return "[PDF: no extractable text]"
	}
	return b.String()
}

func imageMIME(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) > 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
