package session

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"ki/internal/types"
)

func TestQueueEnqueueDequeueAndCap(t *testing.T) {
	dir := t.TempDir()
	item, err := Enqueue(dir, []types.Content{{Type: "text", Text: "a"}})
	if err != nil || item.ID == "" {
		t.Fatalf("enqueue: %+v %v", item, err)
	}
	got, err := ReadQueue(dir)
	if err != nil || len(got) != 1 || got[0].Content[0].Text != "a" {
		t.Fatalf("read: %+v %v", got, err)
	}
	head, ok, err := Dequeue(dir)
	if err != nil || !ok || head.ID != item.ID {
		t.Fatalf("dequeue: %+v %v %v", head, ok, err)
	}
	if leftover, err := ReadQueue(dir); err != nil || len(leftover) != 0 {
		t.Fatalf("after dequeue: %+v %v", leftover, err)
	}

	for i := range MaxQueueItems {
		if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "x"}}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "overflow"}}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("cap: %v", err)
	}
}

func TestQueuePreservesOrigin(t *testing.T) {
	dir := t.TempDir()
	item, err := EnqueueSystem(dir, []types.Content{{Type: "text", Text: "done"}}, "agent:a-1")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := Dequeue(dir)
	if err != nil || !ok {
		t.Fatalf("dequeue = %+v, %v, %v", got, ok, err)
	}
	if got.ID != item.ID || got.Origin != "agent:a-1" {
		t.Fatalf("queued origin = %+v", got)
	}
	if got.Lane != QueueSystemLane {
		t.Fatalf("EnqueueSystem lane = %q, want %q", got.Lane, QueueSystemLane)
	}
	if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "human"}}); err != nil {
		t.Fatal(err)
	}
	items, err := ReadQueue(dir)
	if err != nil || len(items) != 1 || items[0].Lane != QueueHumanLane {
		t.Fatalf("Enqueue lane = %+v, %v", items, err)
	}
}

// A person waiting on a reply must never sit behind a system turn: the
// completion notification was enqueued first, but the human turn is served
// first, and system turns keep their own FIFO order.
func TestDequeueServesHumanTurnFirst(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnqueueSystem(dir, []types.Content{{Type: "text", Text: "notification"}}, "agent:a-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueSystem(dir, []types.Content{{Type: "text", Text: "notification-2"}}, "agent:a-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "human"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"human", "notification", "notification-2"}
	for i, text := range want {
		item, ok, err := Dequeue(dir)
		if err != nil || !ok {
			t.Fatalf("dequeue %d = %+v, %v, %v", i, item, ok, err)
		}
		if got := item.Content[0].Text; got != text {
			t.Fatalf("dequeue %d = %q, want %q", i, got, text)
		}
	}
	if items, err := ReadQueue(dir); err != nil || len(items) != 0 {
		t.Fatalf("queue drained: %+v %v", items, err)
	}
}

func TestDequeueKeepsSystemFIFOWithoutHumanTurn(t *testing.T) {
	dir := t.TempDir()
	for _, text := range []string{"one", "two"} {
		if _, err := EnqueueSystem(dir, []types.Content{{Type: "text", Text: text}}, "agent:a"); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"one", "two"} {
		item, ok, err := Dequeue(dir)
		if err != nil || !ok || item.Content[0].Text != want {
			t.Fatalf("dequeue = %+v, %v, %v; want %q", item, ok, err, want)
		}
	}
}

// An empty lane comes from a queue written before lanes existed. It must read
// as system: the real human turn is still served first, and the legacy item
// keeps its FIFO position among system turns.
func TestDequeueTreatsLegacyItemAsSystem(t *testing.T) {
	dir := t.TempDir()
	legacy := QueuedItem{ID: "legacy", Content: []types.Content{{Type: "text", Text: "legacy"}}}
	if err := writeQueue(dir, []QueuedItem{legacy}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueSystem(dir, []types.Content{{Type: "text", Text: "system"}}, "agent:a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "human"}}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"human", "legacy", "system"} {
		item, ok, err := Dequeue(dir)
		if err != nil || !ok {
			t.Fatalf("dequeue %d = %+v, %v, %v", i, item, ok, err)
		}
		if got := item.Content[0].Text; got != want {
			t.Fatalf("dequeue %d = %q, want %q", i, got, want)
		}
	}
}

// A retry must rejoin its own lane: the human turn keeps outranking the system
// item that was requeued after a failed dispatch.
func TestEnqueueFrontRestoresIntoItsLane(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnqueueSystem(dir, []types.Content{{Type: "text", Text: "system"}}, "agent:a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "human"}}); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueFront(dir, QueuedItem{ID: "retry", Lane: QueueSystemLane, Content: []types.Content{{Type: "text", Text: "retry"}}}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"human", "retry", "system"} {
		item, ok, err := Dequeue(dir)
		if err != nil || !ok {
			t.Fatalf("dequeue %d = %+v, %v, %v", i, item, ok, err)
		}
		if got := item.Content[0].Text; got != want {
			t.Fatalf("dequeue %d = %q, want %q", i, got, want)
		}
	}
}

func TestQueueConcurrentMutationsKeepAllItems(t *testing.T) {
	dir := t.TempDir()
	const count = 50
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "message"}}); err != nil {
				t.Errorf("enqueue: %v", err)
			}
		})
	}
	wg.Wait()
	items, err := ReadQueue(dir)
	if err != nil || len(items) != count {
		t.Fatalf("concurrent enqueue: %d items, err=%v", len(items), err)
	}
	seen := make(map[string]bool, count)
	for _, item := range items {
		if seen[item.ID] {
			t.Fatalf("duplicate queue id %q", item.ID)
		}
		seen[item.ID] = true
	}
	for range count {
		wg.Go(func() {
			if _, _, err := Dequeue(dir); err != nil {
				t.Errorf("dequeue: %v", err)
			}
		})
	}
	wg.Wait()
	if left, err := ReadQueue(dir); err != nil || len(left) != 0 {
		t.Fatalf("concurrent dequeue: %d items, err=%v", len(left), err)
	}
}

func TestTakeQueueIDRemovesMiddle(t *testing.T) {
	dir := t.TempDir()
	a, _ := Enqueue(dir, []types.Content{{Type: "text", Text: "a"}})
	b, _ := Enqueue(dir, []types.Content{{Type: "text", Text: "b"}})
	c, _ := Enqueue(dir, []types.Content{{Type: "text", Text: "c"}})
	got, err := TakeQueueID(dir, b.ID)
	if err != nil || got.ID != b.ID || got.Content[0].Text != "b" {
		t.Fatalf("take: %+v %v", got, err)
	}
	left, err := ReadQueue(dir)
	if err != nil || len(left) != 2 || left[0].ID != a.ID || left[1].ID != c.ID {
		t.Fatalf("left: %+v %v", left, err)
	}
	if _, err := TakeQueueID(dir, b.ID); !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("missing: %v", err)
	}
	empty := t.TempDir()
	if _, err := TakeQueueID(empty, a.ID); !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("empty: %v", err)
	}
}

func TestKeepQueueIDsDeletes(t *testing.T) {
	dir := t.TempDir()
	a, _ := Enqueue(dir, []types.Content{{Type: "text", Text: "a"}})
	b, _ := Enqueue(dir, []types.Content{{Type: "text", Text: "b"}})
	_, _ = Enqueue(dir, []types.Content{{Type: "text", Text: "c"}})
	got, err := KeepQueueIDs(dir, []string{b.ID, a.ID, "missing"})
	if err != nil || len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("keep: %+v %v", got, err)
	}
}

func TestContextQueueDrainsInOrderAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	first, err := EnqueueContext(dir, ContextQueuedItem{
		Message:        types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "first"}}},
		IdempotencyKey: "update-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := EnqueueContext(dir, ContextQueuedItem{
		Message:        types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "duplicate"}}},
		IdempotencyKey: "update-1",
	})
	if err != nil || duplicate.ID != first.ID || duplicate.Sequence != first.Sequence {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	second, err := EnqueueContext(dir, ContextQueuedItem{
		Message: types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "second"}}},
	})
	if err != nil || second.Sequence <= first.Sequence {
		t.Fatalf("second: %+v %v", second, err)
	}

	var got []string
	if err := DrainContextThrough(dir, first.Sequence, func(item ContextQueuedItem) error {
		got = append(got, item.Message.Text())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "first" {
		t.Fatalf("partial drain: %v", got)
	}
	pending, err := ReadContextQueue(dir)
	if err != nil || len(pending) != 1 || pending[0].Message.Text() != "second" {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	if err := DrainContextThrough(dir, 0, func(item ContextQueuedItem) error {
		got = append(got, item.Message.Text())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "first|second" {
		t.Fatalf("drain order: %v", got)
	}
	if next, err := PendingContextSequence(dir); err != nil || next != 0 {
		t.Fatalf("pending sequence after drain: %d %v", next, err)
	}
	third, err := EnqueueContext(dir, ContextQueuedItem{
		Message: types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "third"}}},
	})
	if err != nil || third.Sequence <= second.Sequence {
		t.Fatalf("sequence was not retained: %+v %v", third, err)
	}
}
