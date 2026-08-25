package session

import (
	"testing"

	"ki/internal/types"
)

func TestUserQueueThenExtQueueOrder(t *testing.T) {
	dir := t.TempDir()
	if _, err := osWriteHeader(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(dir, []types.Content{{Type: "text", Text: "user"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueExt(dir, ExtQueuedItem{Content: []types.Content{{Type: "text", Text: "ext"}}, Extension: "goal"}); err != nil {
		t.Fatal(err)
	}
	u, ok, err := Dequeue(dir)
	if err != nil || !ok || u.Content[0].Text != "user" {
		t.Fatalf("user first: %+v %v %v", u, ok, err)
	}
	e, ok, err := DequeueExt(dir)
	if err != nil || !ok || e.Content[0].Text != "ext" {
		t.Fatalf("ext second: %+v %v %v", e, ok, err)
	}
}

func TestNextTurnDoesNotOccupyFromExtQueue(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnqueueExt(dir, ExtQueuedItem{Content: []types.Content{{Type: "text", Text: "later"}}, When: "nextTurn", Extension: "goal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnqueueExt(dir, ExtQueuedItem{Content: []types.Content{{Type: "text", Text: "run"}}, Extension: "goal"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := DequeueExtOccupy(dir)
	if err != nil || !ok || got.Content[0].Text != "run" {
		t.Fatalf("occupy item: %+v %v %v", got, ok, err)
	}
	left, err := ReadExtQueue(dir)
	if err != nil || len(left) != 1 || left[0].When != "nextTurn" {
		t.Fatalf("nextTurn remains: %+v %v", left, err)
	}
	taken, err := TakeNextTurn(dir)
	if err != nil || len(taken) != 1 || taken[0].Content[0].Text != "later" {
		t.Fatalf("take %+v %v", taken, err)
	}
	if left, _ := ReadExtQueue(dir); len(left) != 0 {
		t.Fatalf("queue leftover %+v", left)
	}
}

func osWriteHeader(dir string) (string, error) {
	return dir, nil
}
