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

// QueueLane tells a queued turn apart by who asked for it. Dequeue always
// serves the human lane first; within a lane the queue stays FIFO.
type QueueLane string

const (
	// QueueHumanLane carries turns a person submitted while the session was busy
	// (POST prompt with delivery=queue).
	QueueHumanLane QueueLane = "human"
	// QueueSystemLane carries turns the server generated, such as an agent
	// completion notification or a message an agent sent its caller. An empty
	// lane (a queue written before lanes existed) is read as system.
	QueueSystemLane QueueLane = "system"
)

// QueuedItem is one turn waiting for the current run to finish.
type QueuedItem struct {
	ID      string          `json:"id"`
	Content []types.Content `json:"content"`
	Origin  string          `json:"origin,omitempty"`
	// Lane is persisted: a restart must not promote a system turn ahead of a
	// waiting human one.
	Lane QueueLane `json:"lane,omitempty"`
}

func queuePath(dir string) string { return filepath.Join(dir, "queue.json") }

func extQueuePath(dir string) string { return filepath.Join(dir, "ext-queue.json") }

func contextQueuePath(dir string) string { return filepath.Join(dir, "context-queue.json") }

func queueGate(dir string) *sync.Mutex {
	key := filepath.Clean(dir)
	gate, _ := queueGates.LoadOrStore(key, &sync.Mutex{})
	mu, ok := gate.(*sync.Mutex)
	if !ok {
		mu = &sync.Mutex{}
		queueGates.Store(key, mu)
	}
	return mu
}

// ExtQueuedItem is one extension-origin turn waiting after user queue.
type ExtQueuedItem struct {
	ID             string          `json:"id"`
	Content        []types.Content `json:"content"`
	Extension      string          `json:"extension,omitempty"`
	Kind           string          `json:"kind,omitempty"`
	CustomType     string          `json:"customType,omitempty"`
	When           string          `json:"when,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	// ContextSequence is the last context-only message that belongs before
	// this prompt. It prevents later messages from entering this prompt.
	ContextSequence uint64            `json:"contextSequence,omitempty"`
	ContextBoundary bool              `json:"contextBoundary,omitempty"`
	External        map[string]string `json:"external,omitempty"`
}

// ContextQueuedItem is a normal user message waiting to be committed to the
// session transcript without starting a model run.
type ContextQueuedItem struct {
	ID             string        `json:"id"`
	Sequence       uint64        `json:"sequence"`
	Message        types.Message `json:"message"`
	IdempotencyKey string        `json:"idempotencyKey,omitempty"`
}

type contextQueueState struct {
	Next  uint64              `json:"next"`
	Items []ContextQueuedItem `json:"items"`
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
	return writeFileAtomic(extQueuePath(dir), append(b, '\n'))
}

func readContextQueue(dir string) (contextQueueState, error) {
	b, err := os.ReadFile(contextQueuePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return contextQueueState{}, nil
		}
		return contextQueueState{}, fmt.Errorf("read context queue: %w", err)
	}
	var state contextQueueState
	if err := json.Unmarshal(b, &state); err != nil {
		return contextQueueState{}, fmt.Errorf("decode context queue: %w", err)
	}
	return state, nil
}

func writeContextQueue(dir string, state contextQueueState) error {
	// Keep an empty state after the first write so the sequence remains
	// monotonic across drains; prompt boundaries may still reference an older
	// sequence after the queue has temporarily become empty.
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode context queue: %w", err)
	}
	return writeFileAtomic(contextQueuePath(dir), append(b, '\n'))
}

// EnqueueContext appends a context-only message and assigns a durable order.
func EnqueueContext(dir string, item ContextQueuedItem) (ContextQueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	state, err := readContextQueue(dir)
	if err != nil {
		return ContextQueuedItem{}, err
	}
	if item.IdempotencyKey != "" {
		for _, existing := range state.Items {
			if existing.IdempotencyKey == item.IdempotencyKey {
				return existing, nil
			}
		}
	}
	if len(state.Items) >= MaxQueueItems {
		return ContextQueuedItem{}, ErrQueueFull
	}
	state.Next++
	item.Sequence = state.Next
	if item.ID == "" {
		id, err := idgen.NewV7()
		if err != nil {
			return ContextQueuedItem{}, fmt.Errorf("context queue id: %w", err)
		}
		item.ID = id
	}
	state.Items = append(state.Items, item)
	if err := writeContextQueue(dir, state); err != nil {
		return ContextQueuedItem{}, err
	}
	return item, nil
}

// PendingContextSequence returns the greatest sequence still waiting to be
// committed. Removed entries are already present in the session transcript.
func PendingContextSequence(dir string) (uint64, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	state, err := readContextQueue(dir)
	if err != nil {
		return 0, err
	}
	if len(state.Items) == 0 {
		return 0, nil
	}
	return state.Items[len(state.Items)-1].Sequence, nil
}

// DrainContextThrough gives context-only messages to consume in order and
// removes each item only after the consumer succeeds. A crash between the
// consumer and queue rewrite is safe when the consumer is idempotent.
func DrainContextThrough(dir string, maxSequence uint64, consume func(ContextQueuedItem) error) error {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	state, err := readContextQueue(dir)
	if err != nil {
		return err
	}
	for len(state.Items) > 0 {
		item := state.Items[0]
		if maxSequence != 0 && item.Sequence > maxSequence {
			break
		}
		if err := consume(item); err != nil {
			return err
		}
		state.Items = state.Items[1:]
		if err := writeContextQueue(dir, state); err != nil {
			return err
		}
	}
	return nil
}

// ReadContextQueue returns pending context-only messages.
func ReadContextQueue(dir string) ([]ContextQueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	state, err := readContextQueue(dir)
	if err != nil {
		return nil, err
	}
	return append([]ContextQueuedItem(nil), state.Items...), nil
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

// EnqueueExtFront restores an extension item after dispatch could not start it.
func EnqueueExtFront(dir string, item ExtQueuedItem) error {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	items, err := readExtQueue(dir)
	if err != nil {
		return err
	}
	if len(items) >= MaxQueueItems {
		return ErrQueueFull
	}
	return writeExtQueue(dir, append([]ExtQueuedItem{item}, items...))
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
	return writeFileAtomic(queuePath(dir), append(b, '\n'))
}

// Enqueue appends a human turn. The session directory must already exist.
func Enqueue(dir string, content []types.Content) (QueuedItem, error) {
	return enqueue(dir, content, "", QueueHumanLane)
}

// EnqueueSystem appends a server-generated turn and preserves its origin. The
// session directory must already exist.
func EnqueueSystem(dir string, content []types.Content, origin string) (QueuedItem, error) {
	return enqueue(dir, content, origin, QueueSystemLane)
}

func enqueue(dir string, content []types.Content, origin string, lane QueueLane) (QueuedItem, error) {
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
	item := QueuedItem{ID: id, Content: content, Origin: origin, Lane: lane}
	if err := writeQueue(dir, insertByLane(items, item, false)); err != nil {
		return QueuedItem{}, err
	}
	return item, nil
}

// insertByLane keeps the queue file ordered the way Dequeue serves it — human
// turns ahead of system turns, FIFO within a lane — so the client's list shows
// the real dispatch order. A new turn joins the end of its lane; a retry
// (front) rejoins the head of its lane, because it was dequeued before anything
// still waiting.
func insertByLane(items []QueuedItem, item QueuedItem, front bool) []QueuedItem {
	humans := 0
	for _, it := range items {
		if it.Lane == QueueHumanLane {
			humans++
		}
	}
	idx := len(items)
	switch {
	case item.Lane == QueueHumanLane:
		// End of the human region; a retry goes before every waiting human.
		idx = humans
		if front {
			idx = 0
		}
	case front:
		// Front of the system region: humans stay ahead of the retry.
		idx = humans
	}
	ordered := make([]QueuedItem, 0, len(items)+1)
	ordered = append(ordered, items[:idx]...)
	ordered = append(ordered, item)
	ordered = append(ordered, items[idx:]...)
	return ordered
}

// EnqueueFront restores an item after dispatch could not start it, at the head
// of its own lane. The item was dequeued before anything still queued, so it
// stays first within its lane; a human turn that arrived in the meantime keeps
// its priority over a system retry.
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
	return writeQueue(dir, insertByLane(items, item, true))
}

// Dequeue removes and returns the next turn: the oldest waiting human turn, or
// the oldest system turn when no person is waiting.
//
// Why not plain FIFO: completion notifications are enqueued from a child's
// goroutine and dispatched as soon as the session is idle, so a notification
// that lands a moment before a user message would otherwise start its own turn
// first and leave the person waiting behind a system message. Human input
// outranking system turns is the same rule as Claude Code's queue, where
// pending notifications take the lowest priority so user input is never
// starved. FIFO still holds within each lane, so notifications never reorder
// among themselves.
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
	idx := 0
	for i, item := range items {
		if item.Lane == QueueHumanLane {
			idx = i
			break
		}
	}
	head := items[idx]
	rest := append(append([]QueuedItem{}, items[:idx]...), items[idx+1:]...)
	if err := writeQueue(dir, rest); err != nil {
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
