package loop

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"ki/internal/types"
)

type MessagePatch struct {
	BaseSeq int64        `json:"baseSeq"`
	Changes []WireChange `json:"changes"`
}

type WireChange struct {
	Path  []string        `json:"path"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value,omitempty"`
}

type MessageEncoder struct {
	previous any
	seq      int64
	stream   int64
}

func (c *MessageEncoder) Encode(ev Event) Event {
	if ev.Type == MessageStart || ev.Type == MessageEnd || ev.Type == AgentEnd {
		*c = MessageEncoder{}
	}
	if ev.Type != MessageUpdate || ev.Message == nil {
		return ev
	}
	next, err := messageWireValue(*ev.Message)
	if err != nil {
		return ev
	}
	ev.AssistantMessageEvent = nil
	if c.previous == nil {
		c.stream = ev.Seq
	} else {
		patch := &MessagePatch{BaseSeq: c.seq, Changes: []WireChange{}}
		diffWire(c.previous, next, []string{}, &patch.Changes)
		encoded, err := json.Marshal(patch)
		smaller := err == nil && len(encoded) < wireSizeLowerBound(next)
		if err == nil && !smaller {
			raw, snapshotErr := json.Marshal(next)
			smaller = snapshotErr == nil && len(encoded) < len(raw)
		}
		if smaller {
			ev.Message = nil
			ev.MessagePatch = patch
		}
	}
	ev.MessageStream = c.stream
	c.previous, c.seq = next, ev.Seq
	return ev
}

func diffWire(old, next any, path []string, changes *[]WireChange) {
	child := func(key string) []string { return append(append([]string{}, path...), key) }
	if a, ok := old.(map[string]any); ok {
		if b, ok := next.(map[string]any); ok {
			for key := range a {
				if _, exists := b[key]; !exists {
					*changes = append(*changes, WireChange{Path: child(key), Op: "remove"})
				}
			}
			for key, value := range b {
				if old, exists := a[key]; exists {
					diffWire(old, value, child(key), changes)
				} else {
					raw, _ := json.Marshal(value)
					*changes = append(*changes, WireChange{Path: child(key), Op: "set", Value: raw})
				}
			}
			return
		}
	}
	if a, ok := old.([]any); ok {
		if b, ok := next.([]any); ok {
			if len(a) != len(b) {
				raw, _ := json.Marshal(len(b))
				*changes = append(*changes, WireChange{Path: path, Op: "resize", Value: raw})
			}
			for i := range b {
				var old any
				if i < len(a) {
					old = a[i]
				}
				diffWire(old, b[i], child(strconv.Itoa(i)), changes)
			}
			return
		}
	}
	op, value := "set", next
	if a, ok := old.(string); ok {
		if b, ok := next.(string); ok {
			if a == b {
				return
			}
			if a != "" && strings.HasPrefix(b, a) {
				op, value = "append", b[len(a):]
			}
		}
	} else if reflect.DeepEqual(old, next) {
		return
	}
	raw, _ := json.Marshal(value)
	*changes = append(*changes, WireChange{Path: path, Op: op, Value: raw})
}

type MessageDecoder struct {
	previous any
	seq      int64
	stream   int64
}

func (c *MessageDecoder) Decode(ev Event) (Event, error) {
	if ev.Type == MessageStart || ev.Type == MessageEnd || ev.Type == AgentEnd {
		*c = MessageDecoder{}
	}
	if ev.Type != MessageUpdate {
		return ev, nil
	}
	if p := ev.MessagePatch; p != nil {
		if c.previous == nil || p.BaseSeq != c.seq || ev.MessageStream != c.stream {
			return ev, fmt.Errorf("message patch base mismatch")
		}
		raw, _ := json.Marshal(c.previous)
		var next any
		_ = json.Unmarshal(raw, &next)
		for _, change := range p.Changes {
			var err error
			next, err = applyWire(next, change.Path, change)
			if err != nil {
				return ev, err
			}
		}
		raw, _ = json.Marshal(next)
		var message types.Message
		if err := json.Unmarshal(raw, &message); err != nil {
			return ev, err
		}
		ev.Message, ev.MessagePatch = &message, nil
	}
	if ev.Message != nil {
		raw, err := json.Marshal(ev.Message)
		if err != nil {
			return ev, err
		}
		var previous any
		if err := json.Unmarshal(raw, &previous); err != nil {
			return ev, err
		}
		c.previous = previous
		c.seq, c.stream = ev.Seq, ev.MessageStream
	}
	return ev, nil
}

func applyWire(node any, path []string, change WireChange) (any, error) {
	if len(path) == 0 {
		var value any
		if err := json.Unmarshal(change.Value, &value); err != nil {
			return nil, err
		}
		switch change.Op {
		case "set":
			return value, nil
		case "append":
			a, aok := node.(string)
			b, bok := value.(string)
			if aok && bok {
				return a + b, nil
			}
		case "resize":
			a, aok := node.([]any)
			n, nok := value.(float64)
			if aok && nok && n >= 0 && n <= 1_000_000 && n == float64(int(n)) {
				out := make([]any, int(n))
				copy(out, a)
				return out, nil
			}
		}
		return nil, fmt.Errorf("invalid message patch operation")
	}
	key := path[0]
	switch parent := node.(type) {
	case map[string]any:
		if len(path) == 1 && change.Op == "remove" {
			delete(parent, key)
			return parent, nil
		}
		value, err := applyWire(parent[key], path[1:], change)
		if err != nil {
			return nil, err
		}
		parent[key] = value
		return parent, nil
	case []any:
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(parent) {
			return nil, fmt.Errorf("invalid message patch index")
		}
		value, err := applyWire(parent[i], path[1:], change)
		if err != nil {
			return nil, err
		}
		parent[i] = value
		return parent, nil
	}
	return nil, fmt.Errorf("invalid message patch path")
}
