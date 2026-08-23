package idgen

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewV7(t *testing.T) {
	id, err := NewV7()
	if err != nil {
		t.Fatalf("NewV7() error = %v", err)
	}
	if len(id) != 32 || strings.ContainsRune(id, '-') {
		t.Fatalf("id = %q, want 32 hex characters without hyphens", id)
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("uuid.Parse(%q) error = %v", id, err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("version = %d, want 7", parsed.Version())
	}
	if parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("variant = %v, want RFC4122", parsed.Variant())
	}
}

func TestEntryIDConcurrentUnique(t *testing.T) {
	const count = 1000
	ids := make(chan string, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			id, err := EntryID()
			if err != nil {
				t.Errorf("EntryID() error = %v", err)
				return
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]struct{}, count)
	for id := range ids {
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate entry id %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("got %d ids, want %d", len(seen), count)
	}
}

func TestFileTimestamp(t *testing.T) {
	got := FileTimestamp(time.Date(2026, 8, 18, 12, 34, 56, 123456789, time.FixedZone("test", 8*60*60)))
	if want := "2026-08-18T04-34-56-123456789Z"; got != want {
		t.Fatalf("FileTimestamp() = %q, want %q", got, want)
	}
}
