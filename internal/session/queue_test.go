package session

import (
	"errors"
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

	for i := 0; i < MaxQueueItems; i++ {
		if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "x"}}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "overflow"}}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("cap: %v", err)
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
