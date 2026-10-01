package process

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputSanitizerHandlesSplitANSIAndControls(t *testing.T) {
	var sanitizer outputSanitizer
	first := sanitizer.Filter([]byte("a\x1b[3"))
	second := sanitizer.Filter([]byte("1mred\x1b[0m\x01\t\n"))
	if got := string(append(first, second...)); got != "ared\t\n" {
		t.Fatalf("sanitized = %q", got)
	}
}

func TestTruncateTailBoundsLongUTF8Line(t *testing.T) {
	out, note := truncateTail(strings.Repeat("你", maxBytes))
	if len(out) > maxBytes || !utf8.ValidString(out) || note == "" {
		t.Fatal("UTF-8 tail truncation contract")
	}
}
