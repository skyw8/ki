package memory

import (
	"strings"
	"testing"
)

func TestWeightChargesSharedGraphsOnce(t *testing.T) {
	text := strings.Repeat("schema", 1000)
	shared := map[string]any{"text": text, "nested": map[string]any{"type": "object"}}
	one := Weight([]any{shared})
	two := Weight([]any{shared, shared})
	if two-one > 64 {
		t.Fatalf("shared map charged twice: %d/%d", one, two)
	}
	copy := map[string]any{"text": strings.Clone(text), "nested": map[string]any{"type": "object"}}
	if Weight([]any{shared, copy}) <= two+int64(len(text)) {
		t.Fatal("independent maps/storage not charged")
	}
	type node struct {
		Next *node
		Text string
	}
	n := &node{Text: text}
	n.Next = n
	if Weight(n) < int64(len(text)) {
		t.Fatal("cyclic graph/string lost")
	}
}

func TestWeightChargesGrowingSharedStringToItsLongestPrefix(t *testing.T) {
	text := strings.Repeat("partial", 1000)
	shortFirst := Weight([]string{text[:10], text})
	longFirst := Weight([]string{text, text[:10]})
	if shortFirst != longFirst || shortFirst < int64(len(text)) {
		t.Fatalf("shared prefix undercounted: %d/%d", shortFirst, longFirst)
	}
}

func TestWeightChargesGrowingSliceAndCycles(t *testing.T) {
	values := []any{"short", strings.Repeat("large", 1000)}
	a := Weight([]any{values[:1], values})
	b := Weight([]any{values, values[:1]})
	if a != b || a < 5000 {
		t.Fatalf("shared slice undercounted: %d/%d", a, b)
	}
	cycle := make([]any, 1)
	cycle[0] = cycle
	if Weight(cycle) == 0 {
		t.Fatal("slice cycle lost its backing storage")
	}
}
