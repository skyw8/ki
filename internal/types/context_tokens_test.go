package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageTokensCountsArgumentsAndBoundedMedia(t *testing.T) {
	m := Message{Role: "assistant", Content: []Content{{Type: "toolCall", Name: "read", Arguments: map[string]any{"path": strings.Repeat("x", 400)}}}}
	if got := MessageTokens(m); got < 100 {
		t.Fatalf("arguments not counted: %d", got)
	}
	image := Message{Content: []Content{{Type: "image", Data: strings.Repeat("binary", 1<<18)}}}
	first := MessageTokens(image)
	image.Content[0].Data = "small"
	if got := MessageTokens(image); got != first || got == 0 {
		t.Fatalf("media size leaked into token count: %d vs %d", got, first)
	}
	m.Timestamp = 1000
	m.Details = map[string]any{"large": strings.Repeat("metadata", 10000)}
	if got := MessageTokens(m); got > 120 {
		t.Fatalf("metadata counted: %d", got)
	}
}

func TestResponseItemTokensCountsContentNotEnvelope(t *testing.T) {
	first := ResponseItemTokens(json.RawMessage(`{"type":"message","id":"one","content":[{"type":"input_text","text":"abcd"}]}`))
	second := ResponseItemTokens(json.RawMessage(`{"type":"message","id":"much-longer-metadata","content":[{"type":"input_text","text":"abcd"}]}`))
	if first != 1 || first != second {
		t.Fatalf("envelope affected count: %d %d", first, second)
	}
}
