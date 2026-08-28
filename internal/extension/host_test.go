package extension

import "testing"

func TestUITextHelpersKeepStructuredValuesOpaque(t *testing.T) {
	if !UITextEmpty(nil) || !UITextEmpty("") {
		t.Fatal("nil and empty strings should clear text")
	}
	text := map[string]any{"key": "status.ready", "fallback": "Ready"}
	if UITextEmpty(text) {
		t.Fatal("translation descriptor should remain visible")
	}
	if got := UITextFallback(text); got != "Ready" {
		t.Fatalf("fallback = %q", got)
	}
	if got := UITextFallback(map[string]any{"key": "status.ready"}); got != "status.ready" {
		t.Fatalf("key fallback = %q", got)
	}
}
