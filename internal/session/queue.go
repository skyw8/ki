package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"ki/internal/idgen"
	"ki/internal/types"
)

// MaxQueueItems is the per-session FIFO cap.
const MaxQueueItems = 100

var (
	// ErrQueueFull means enqueue was refused because the session already has MaxQueueItems.
	ErrQueueFull = errors.New("session queue is full")
	// ErrQueueItemNotFound means TakeQueueID did not match a live item.
	ErrQueueItemNotFound = errors.New("queue item not found")
	// Why: completion notifications and queue dispatch can mutate one FIFO
	// concurrently; serializing the read-modify-write protects items in the
	// same serve process without adding another API or route.
	queueGates sync.Map // map[clean session dir]*sync.Mutex
)

// QueuedItem is one user turn waiting for the current run to finish.
type QueuedItem struct {
	ID      string          `json:"id"`
	Content []types.Content `json:"content"`
}

func queuePath(dir string) string { return filepath.Join(dir, "queue.json") }

func extQueuePath(dir string) string { return filepath.Join(dir, "ext-queue.json") }

func queueGate(dir string) *sync.Mutex {
	key := filepath.Clean(dir)
	gate, _ := queueGates.LoadOrStore(key, &sync.Mutex{})
	return gate.(*sync.Mutex)
}

// ExtQueuedItem is one extension-origin turn waiting after user queue.
type ExtQueuedItem struct {
	ID         string          `json:"id"`
	Content    []types.Content `json:"content"`
	Extension  string          `json:"extension,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	CustomType string          `json:"customType,omitempty"`
	When       string          `json:"when,omitempty"`
}

func readExtQueue(dir string) ([]ExtQueuedItem, error) {
	b, err := os.ReadFile(extQueuePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ext-queue: %w", err)
	}
	var items []ExtQueuedItem
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("decode ext-queue: %w", err)
	}
	return items, nil
}

func writeExtQueue(dir string, items []ExtQueuedItem) error {
	if len(items) == 0 {
		if err := os.Remove(extQueuePath(dir)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove ext-queue: %w", err)
		}
		return nil
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ext-queue: %w", err)
	}
	return os.WriteFile(extQueuePath(dir), append(b, '\n'), 0o600)
}

// EnqueueExt appends an extension FIFO item.
func EnqueueExt(dir string, item ExtQueuedItem) (ExtQueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readExtQueue(dir)
	if err != nil {
		return ExtQueuedItem{}, err
	}
	if len(items) >= MaxQueueItems {
		return ExtQueuedItem{}, ErrQueueFull
	}
	if item.ID == "" {
		id, err := idgen.NewV7()
		if err != nil {
			return ExtQueuedItem{}, fmt.Errorf("ext-queue id: %w", err)
		}
		item.ID = id
	}
	items = append(items, item)
	if err := writeExtQueue(dir, items); err != nil {
		return ExtQueuedItem{}, err
	}
	return item, nil
}

// DequeueExt removes the head extension FIFO item.
func DequeueExt(dir string) (ExtQueuedItem, bool, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readExtQueue(dir)
	if err != nil {
		return ExtQueuedItem{}, false, err
	}
	if len(items) == 0 {
		return ExtQueuedItem{}, false, nil
	}
	head := items[0]
	if err := writeExtQueue(dir, items[1:]); err != nil {
		return ExtQueuedItem{}, false, err
	}
	return head, true, nil
}

// DequeueExtOccupy removes the first item that may start an occupy.
// nextTurn items stay put until a user occupy consumes them.
func DequeueExtOccupy(dir string) (ExtQueuedItem, bool, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readExtQueue(dir)
	if err != nil {
		return ExtQueuedItem{}, false, err
	}
	idx := -1
	for i, it := range items {
		if it.When != "nextTurn" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ExtQueuedItem{}, false, nil
	}
	head := items[idx]
	rest := append(append([]ExtQueuedItem{}, items[:idx]...), items[idx+1:]...)
	if err := writeExtQueue(dir, rest); err != nil {
		return ExtQueuedItem{}, false, err
	}
	return head, true, nil
}

// TakeNextTurn removes every nextTurn item. User occupy injects them; they never start a run.
func TakeNextTurn(dir string) ([]ExtQueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readExtQueue(dir)
	if err != nil {
		return nil, err
	}
	var taken, rest []ExtQueuedItem
	for _, it := range items {
		if it.When == "nextTurn" {
			taken = append(taken, it)
			continue
		}
		rest = append(rest, it)
	}
	if len(taken) == 0 {
		return nil, nil
	}
	if err := writeExtQueue(dir, rest); err != nil {
		return nil, err
	}
	return taken, nil
}

// ReadExtQueue returns the extension FIFO.
func ReadExtQueue(dir string) ([]ExtQueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	return readExtQueue(dir)
}

// ReadQueue loads the durable FIFO. A missing file is empty.
func ReadQueue(dir string) ([]QueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	return readQueue(dir)
}

func readQueue(dir string) ([]QueuedItem, error) {
	b, err := os.ReadFile(queuePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read queue: %w", err)
	}
	var items []QueuedItem
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("decode queue: %w", err)
	}
	return items, nil
}

func writeQueue(dir string, items []QueuedItem) error {
	if len(items) == 0 {
		if err := os.Remove(queuePath(dir)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove queue: %w", err)
		}
		return nil
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("encode queue: %w", err)
	}
	return os.WriteFile(queuePath(dir), append(b, '\n'), 0o600)
}

// Enqueue appends a user turn. The session directory must already exist.
func Enqueue(dir string, content []types.Content) (QueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readQueue(dir)
	if err != nil {
		return QueuedItem{}, err
	}
	if len(items) >= MaxQueueItems {
		return QueuedItem{}, ErrQueueFull
	}
	id, err := idgen.NewV7()
	if err != nil {
		return QueuedItem{}, fmt.Errorf("queue id: %w", err)
	}
	item := QueuedItem{ID: id, Content: content}
	items = append(items, item)
	if err := writeQueue(dir, items); err != nil {
		return QueuedItem{}, err
	}
	return item, nil
}

// EnqueueFront puts an item back at the head after a failed dispatch.
func EnqueueFront(dir string, item QueuedItem) error {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readQueue(dir)
	if err != nil {
		return err
	}
	if len(items) >= MaxQueueItems {
		return ErrQueueFull
	}
	return writeQueue(dir, append([]QueuedItem{item}, items...))
}

// Dequeue removes and returns the head item.
func Dequeue(dir string) (QueuedItem, bool, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readQueue(dir)
	if err != nil {
		return QueuedItem{}, false, err
	}
	if len(items) == 0 {
		return QueuedItem{}, false, nil
	}
	head := items[0]
	if err := writeQueue(dir, items[1:]); err != nil {
		return QueuedItem{}, false, err
	}
	return head, true, nil
}

// TakeQueueID removes one item by id. Remaining items keep their order.
func TakeQueueID(dir, id string) (QueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readQueue(dir)
	if err != nil {
		return QueuedItem{}, err
	}
	idx := -1
	for i, item := range items {
		if item.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return QueuedItem{}, ErrQueueItemNotFound
	}
	item := items[idx]
	rest := append(append([]QueuedItem{}, items[:idx]...), items[idx+1:]...)
	if err := writeQueue(dir, rest); err != nil {
		return QueuedItem{}, err
	}
	return item, nil
}

// KeepQueueIDs replaces the queue with the listed ids in that order. Unknown
// ids are skipped. This is the v1 delete/reorder-by-subset API.
func KeepQueueIDs(dir string, ids []string) ([]QueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readQueue(dir)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]QueuedItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	out := make([]QueuedItem, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		item, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, item)
	}
	if err := writeQueue(dir, out); err != nil {
		return nil, err
	}
	return out, nil
}
