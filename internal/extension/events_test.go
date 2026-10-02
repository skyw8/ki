package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func eventTestClient(w io.Writer, events ...string) *rpcClient {
	c := &rpcClient{enc: json.NewEncoder(w)}
	c.registration.asyncEvents = make(map[string]bool, len(events))
	for _, event := range events {
		c.registration.asyncEvents[event] = true
	}
	return c
}

func TestManagerEventFanout(t *testing.T) {
	var first, second, other bytes.Buffer
	m := NewManager("", nil)
	m.by["first"] = eventTestClient(&first, EventMessageUpdate)
	m.by["second"] = eventTestClient(&second, EventMessageUpdate)
	m.by["other"] = eventTestClient(&other, EventAgentEnd)
	m.order["session"] = []string{"first", "other", "first", "second", "missing"}
	for _, text := range []string{"first <&>\n中文", "second"} {
		m.OnEvent(t.Context(), "session", Event{Type: EventMessageUpdate, SessionID: "session", Text: text})
	}
	if first.String() != second.String() {
		t.Fatal("subscribers received different frames")
	}
	if other.Len() != 0 {
		t.Fatal("unsubscribed client received an event")
	}
	decoder := json.NewDecoder(&first)
	for _, text := range []string{"first <&>\n中文", "second"} {
		var msg rpcMsg
		if err := decoder.Decode(&msg); err != nil {
			t.Fatal(err)
		}
		var params struct {
			SessionID string `json:"sessionId"`
			Event     string `json:"event"`
			Payload   Event  `json:"payload"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			t.Fatal(err)
		}
		if msg.JSONRPC != "2.0" || msg.Method != "lifecycle.event" ||
			params.SessionID != "session" || params.Event != EventMessageUpdate || params.Payload.Text != text {
			t.Fatalf("unexpected frame: %s", msg.Params)
		}
	}
	var extra rpcMsg
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("duplicate delivery: %v", err)
	}
}

func BenchmarkManagerEventFanout(b *testing.B) {
	for _, subscribers := range []int{0, 1, 4} {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			m := NewManager("", nil)
			for i := range subscribers {
				name := fmt.Sprint(i)
				m.by[name] = eventTestClient(io.Discard, EventMessageUpdate)
				m.order["session"] = append(m.order["session"], name)
			}
			ev := Event{Type: EventMessageUpdate, SessionID: "session", Text: strings.Repeat("x", 32*1024)}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				m.OnEvent(ctx, "session", ev)
			}
		})
	}
}
