package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestTranscriptConformance makes the browser's synthetic recovery fixtures a
// checked wire contract, not a manually maintained approximation of compact
// responses. Both Go and Bun consume these inputs and exact projected outputs.
func TestTranscriptConformance(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "unit", "fixtures")
	var input struct {
		Histories map[string][]Entry `json:"histories"`
		Cases     map[string]struct {
			History string `json:"history"`
			Leaf    string `json:"leaf"`
			Before  string `json:"before"`
			Keep    int    `json:"keep"`
		} `json:"cases"`
	}
	readConformanceJSON(t, filepath.Join(dir, "transcript-recovery-inputs.json"), &input)
	if len(input.Cases) == 0 {
		t.Fatal("conformance inputs must contain cases")
	}
	actual := make(map[string]CompactPage, len(input.Cases))
	used := make(map[string]bool)
	for name, tc := range input.Cases {
		history, ok := input.Histories[tc.History]
		if !ok || len(history) == 0 {
			t.Fatalf("%s: missing/empty history %q", name, tc.History)
		}
		used[tc.History] = true
		actual[name] = BuildCompact(history, tc.Leaf, tc.Before, tc.Keep)
	}
	for name := range input.Histories {
		if !used[name] {
			t.Errorf("history %q has no conformance case", name)
		}
	}
	path := filepath.Join(dir, "transcript-recovery.json")
	// Regeneration and verification deliberately use the same case loader.
	// Normal CI never sets this explicit opt-in and must fail on stale output.
	if os.Getenv("KI_UPDATE_TRANSCRIPT_FIXTURES") == "1" {
		raw, err := json.MarshalIndent(actual, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var expected map[string]json.RawMessage
	readConformanceJSON(t, path, &expected)
	for name := range expected {
		if _, ok := actual[name]; !ok {
			t.Errorf("output %q has no conformance input", name)
		}
	}
	for name, page := range actual {
		t.Run(name, func(t *testing.T) {
			want, ok := expected[name]
			if !ok {
				t.Fatal("missing expected wire output; regenerate transcript-recovery fixtures")
			}
			raw, err := json.Marshal(page)
			if err != nil {
				t.Fatal(err)
			}
			var gotJSON, wantJSON any
			if err := json.Unmarshal(raw, &gotJSON); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &wantJSON); err != nil {
				t.Fatal(err)
			}
			// Compare wire JSON, not decoded CompactPage values: an added
			// zero-valued wire field must invalidate the browser fixture too.
			if !reflect.DeepEqual(gotJSON, wantJSON) {
				got, _ := json.MarshalIndent(gotJSON, "", "  ")
				want, _ := json.MarshalIndent(wantJSON, "", "  ")
				t.Errorf("stale transcript fixture (go run ./web/unit/fixtures/transcript-recovery.go from repository root)\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}

func readConformanceJSON(t *testing.T, path string, dst any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
