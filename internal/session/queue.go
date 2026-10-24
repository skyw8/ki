package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"ki/internal/idgen"
	"ki/internal/state"
	"ki/internal/types"
)

const MaxQueueItems = 100

var (
	ErrQueueFull         = errors.New("session queue is full")
	ErrQueueItemNotFound = errors.New("queue item not found")
	queueGates           sync.Map
)

type QueueLane string

const (
	QueueHumanLane  QueueLane = "human"
	QueueSystemLane QueueLane = "system"
)

type QueuedItem struct {
	ID              string                    `json:"id"`
	Content         []types.Content           `json:"content"`
	Origin          string                    `json:"origin,omitempty"`
	ClientRequestID string                    `json:"clientRequestId,omitempty"`
	Completion      *types.CompletionIdentity `json:"completion,omitempty"`
	Lane            QueueLane                 `json:"lane,omitempty"`
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

type ExtQueuedItem struct {
	ID              string            `json:"id"`
	Content         []types.Content   `json:"content"`
	Extension       string            `json:"extension,omitempty"`
	Kind            string            `json:"kind,omitempty"`
	CustomType      string            `json:"customType,omitempty"`
	When            string            `json:"when,omitempty"`
	IdempotencyKey  string            `json:"idempotencyKey,omitempty"`
	ContextSequence uint64            `json:"contextSequence,omitzero"`
	ContextBoundary bool              `json:"contextBoundary,omitzero"`
	External        map[string]string `json:"external,omitempty"`
}

type ContextQueuedItem struct {
	ID             string        `json:"id"`
	Sequence       uint64        `json:"sequence"`
	Message        types.Message `json:"message"`
	IdempotencyKey string        `json:"idempotencyKey,omitempty"`
}

type contextQueueState struct {
	Version int                 `json:"version"`
	Next    uint64              `json:"next"`
	Items   []ContextQueuedItem `json:"items"`
}

func readExtQueue(dir string) ([]ExtQueuedItem, error) {
	b, _, err := state.ReadFile(extQueuePath(dir), 1, nil)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ext-queue: %w", err)
	}
	var doc struct {
		Items []ExtQueuedItem `json:"items"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("decode ext-queue: %w", err)
	}
	return doc.Items, nil
}

func writeExtQueue(dir string, items []ExtQueuedItem) error {
	if len(items) == 0 {
		if err := os.Remove(extQueuePath(dir)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove ext-queue: %w", err)
		}
		return nil
	}
	return state.WriteVersioned(extQueuePath(dir), 1, struct {
		Version int             `json:"version"`
		Items   []ExtQueuedItem `json:"items"`
	}{1, items}, 0o600)
}

func readContextQueue(dir string) (contextQueueState, error) {
	b, _, err := state.ReadFile(contextQueuePath(dir), 1, nil)
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

func writeContextQueue(dir string, doc contextQueueState) error {
	doc.Version = 1
	return state.WriteVersioned(contextQueuePath(dir), 1, doc, 0o600)
}

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
	if item.Message.ClientRequestID == "" {
		item.Message.ClientRequestID = item.ID
	}
	state.Items = append(state.Items, item)
	if err := writeContextQueue(dir, state); err != nil {
		return ContextQueuedItem{}, err
	}
	return item, nil
}

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

func ReadContextQueue(dir string) ([]ContextQueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	state, err := readContextQueue(dir)
	if err != nil {
		return nil, err
	}
	return slices.Clone(state.Items), nil
}

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

func ReadExtQueue(dir string) ([]ExtQueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	return readExtQueue(dir)
}

func ReadQueue(dir string) ([]QueuedItem, error) {
	gate := queueGate(dir)
	gate.Lock()
	defer gate.Unlock()
	return readQueue(dir)
}

func readQueue(dir string) ([]QueuedItem, error) {
	b, _, err := state.ReadFile(queuePath(dir), 1, nil)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read queue: %w", err)
	}
	var doc struct {
		Items []QueuedItem `json:"items"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("decode queue: %w", err)
	}
	return doc.Items, nil
}

func writeQueue(dir string, items []QueuedItem) error {
	if len(items) == 0 {
		if err := os.Remove(queuePath(dir)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove queue: %w", err)
		}
		return nil
	}
	return state.WriteVersioned(queuePath(dir), 1, struct {
		Version int          `json:"version"`
		Items   []QueuedItem `json:"items"`
	}{1, items}, 0o600)
}

func Enqueue(dir string, content []types.Content) (QueuedItem, error) {
	return EnqueueMessage(dir, types.Message{Content: content}, QueueHumanLane)
}

func EnqueueSystem(dir string, content []types.Content, origin string) (QueuedItem, error) {
	return EnqueueMessage(dir, types.Message{Content: content, Origin: origin}, QueueSystemLane)
}

func EnqueueAgentNotification(dir string, content []types.Content, origin string, completion types.CompletionIdentity) (QueuedItem, error) {
	return EnqueueMessage(dir, types.Message{Content: content, Origin: origin, Completion: &completion,
		ClientRequestID: fmt.Sprintf("%s:%d", completion.TaskID, completion.Generation)}, QueueSystemLane)
}

func EnqueueMessage(dir string, message types.Message, lane QueueLane) (QueuedItem, error) {
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
	if message.ClientRequestID == "" {
		message.ClientRequestID = id
	}
	item := QueuedItem{ID: id, Content: message.Content, Origin: message.Origin,
		ClientRequestID: message.ClientRequestID, Completion: message.Completion, Lane: lane}
	if err := writeQueue(dir, insertByLane(items, item, false)); err != nil {
		return QueuedItem{}, err
	}
	return item, nil
}

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
		idx = humans
		if front {
			idx = 0
		}
	case front:
		idx = humans
	}
	ordered := make([]QueuedItem, 0, len(items)+1)
	ordered = append(ordered, items[:idx]...)
	ordered = append(ordered, item)
	ordered = append(ordered, items[idx:]...)
	return ordered
}

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
