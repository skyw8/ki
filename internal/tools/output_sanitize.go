package tools

import (
	"strings"
	"unicode"
)

type ansiState uint8

const (
	ansiGround ansiState = iota
	ansiEscape
	ansiCSI
	ansiString
	ansiStringEscape
)

type outputSanitizer struct{ state ansiState }

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
	return out
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
