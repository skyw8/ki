package thinking

import "testing"

func TestSplit(t *testing.T) {
	for _, base := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		t.Run(base, func(t *testing.T) {
			if got, fast := Split(base); got != base || fast {
				t.Fatalf("Split(%q) = %q, %v", base, got, fast)
			}
			if got, fast := Split(base + " fast"); got != base || !fast {
				t.Fatalf("Split(%q) = %q, %v", base+" fast", got, fast)
			}
		})
	}
	for _, level := range []string{"", "fast", "unknown fast", "HIGH fast", "high Fast", "high  fast", " high fast", "high fast ", "high-fast", "high fast fast"} {
		if got, fast := Split(level); got != level || fast {
			t.Errorf("Split(%q) = %q, %v; want unchanged, false", level, got, fast)
		}
	}
}
