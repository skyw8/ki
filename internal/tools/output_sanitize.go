package tools

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type ansiState uint8

const (
	ansiGround ansiState = iota
	ansiEscape
	ansiCSI
	ansiString
	ansiStringEscape
)

type outputSanitizer struct {
	state       ansiState
	utf8Pending []byte
}

// Filter is stateful because process writes can split an escape sequence. The
// raw spill file bypasses this filter and remains the lossless source of truth.
func (s *outputSanitizer) Filter(input []byte) []byte {
	out := make([]byte, 0, len(input))
	for _, b := range input {
		switch s.state {
		case ansiGround:
			switch {
			case b == 0x1b:
				s.state = ansiEscape
			case b == '\n' || b == '\t':
				out = append(out, b)
			case b < 0x20 || b == 0x7f:
			default:
				out = append(out, b)
			}
		case ansiEscape:
			switch b {
			case '[':
				s.state = ansiCSI
			case ']', 'P', 'X', '^', '_':
				s.state = ansiString
			default:
				s.state = ansiGround
			}
		case ansiCSI:
			if b >= 0x40 && b <= 0x7e {
				s.state = ansiGround
			}
		case ansiString:
			switch b {
			case 0x07:
				s.state = ansiGround
			case 0x1b:
				s.state = ansiStringEscape
			}
		case ansiStringEscape:
			if b == '\\' {
				s.state = ansiGround
			} else if b != 0x1b {
				s.state = ansiString
			}
		}
	}
	// A pipe/PTY write may end in the middle of a rune. Keep that suffix until
	// the next write so observers never consume a replacement for valid UTF-8.
	data := append(s.utf8Pending, out...)
	s.utf8Pending = nil
	var text strings.Builder
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			s.utf8Pending = append([]byte(nil), data...)
			break
		}
		r, size := utf8.DecodeRune(data)
		text.WriteRune(r)
		data = data[size:]
	}
	return []byte(cleanUnicodeControls(text.String()))
}

func (s *outputSanitizer) Flush() []byte {
	if len(s.utf8Pending) == 0 {
		return nil
	}
	text := strings.ToValidUTF8(string(s.utf8Pending), "\uFFFD")
	s.utf8Pending = nil
	return []byte(text)
}

func cleanUnicodeControls(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, text)
}
