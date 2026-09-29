package session

import (
	"path/filepath"
	"testing"

	"ki/internal/types"
)

func TestSearchUsesActiveLeafAndAgentFilter(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, filepath.Join(root, "work"), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "Needle in active history"}}}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	hits, more, err := Search(root, SearchOptions{Query: "needle", Limit: 10})
	if err != nil || more || len(hits) != 1 || hits[0].ID != s.ID() {
		t.Fatalf("search = %+v, more=%v, err=%v", hits, more, err)
	}
	if hits[0].Snippet != "Needle in active history" {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
}

func TestSearchLimitReportsMore(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		s, err := Create(root, filepath.Join(root, "work"), "p", "m")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "match"}}}); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
	hits, more, err := Search(root, SearchOptions{Query: "MATCH", Limit: 1})
	if err != nil || !more || len(hits) != 1 {
		t.Fatalf("search = %d hits, more=%v, err=%v", len(hits), more, err)
	}
}
