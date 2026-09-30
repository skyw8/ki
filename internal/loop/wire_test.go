package loop

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ki/internal/types"
)

func TestMessageWireProjectionAndMutableBase(t *testing.T) {
	m := types.Message{
		Role: "assistant", Timestamp: 9, API: "responses", Provider: "p", Model: "m", ResponseID: "r",
		StopReason: "error", ErrorMessage: "failed", ToolCallID: "call", ToolName: "Bash", ToolType: "custom",
		ClientRequestID: "request", Completion: &types.CompletionIdentity{TaskID: "a", Generation: 7}, Origin: "extension:test", External: map[string]string{"key": "before"}, IsError: true,
		LatencyMs: 1, TTFTMs: 2, DurationMs: 3, Details: map[string]any{"nested": []any{"before"}},
		Usage: &types.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, TotalTokens: 10, Cost: &types.UsageCost{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Total: 10}},
		Content: []types.Content{{Type: "toolCall", Text: strings.Repeat("中🙂", 100), Data: "data", MIMEType: "image/png", Path: "image.png", Size: 23,
			Thinking: "thinking", ID: "id", Name: "name", ToolType: "custom", Input: "input", ItemID: "item", ArgumentsRaw: "raw",
			ThinkingSignature: "signature", ThinkingData: "opaque", TextSignature: "textsig", StreamIndex: 7,
			Arguments: map[string]any{"nested": map[string]any{"text": "before"}, "__proto__": nil}}},
	}
	projected, err := messageWireValue(m)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := snapshotJSON(m)
	got, _ := snapshotJSON(projected)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection differs: %#v != %#v", got, want)
	}
	var encoder MessageEncoder
	encoder.Encode(Event{Type: MessageUpdate, Seq: 1, Message: &m})
	m.Content[0].Arguments["nested"].(map[string]any)["text"] = "after"
	m.Content[0].Text += "new"
	m.External["key"] = "after"
	m.Details.(map[string]any)["nested"].([]any)[0] = "after"
	m.Usage.Cost.Total = 20
	wire := encoder.Encode(Event{Type: MessageUpdate, Seq: 2, Message: &m})
	base, _ := snapshotJSON(projected)
	var decoder = MessageDecoder{previous: base, seq: 1, stream: 1}
	decoded, err := decoder.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = snapshotJSON(m)
	got, _ = snapshotJSON(decoded.Message)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("mutable provider values changed the retained baseline")
	}
}

func BenchmarkMessageWireEncode(b *testing.B) {
	for _, kib := range []int{8, 64, 256, 1024} {
		b.Run(fmt.Sprintf("%dKiB", kib), func(b *testing.B) {
			events := make([]Event, 128)
			body := strings.Repeat("a", kib*1024)
			for i := range events {
				body += strings.Repeat("b", 64)
				m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: body}}}
				events[i] = Event{Type: MessageUpdate, Seq: int64(i + 1), Message: &m}
			}
			var encoder MessageEncoder
			i := 0
			b.ReportAllocs()
			for b.Loop() {
				if i == 0 {
					encoder = MessageEncoder{}
				}
				if _, err := json.Marshal(encoder.Encode(events[i])); err != nil {
					b.Fatal(err)
				}
				i = (i + 1) % len(events)
			}
		})
	}
}

func TestMessageWireRoundTrip(t *testing.T) {
	var encoder MessageEncoder
	var decoder MessageDecoder
	long := strings.Repeat("中文🙂", 500)
	messages := []types.Message{
		{Role: "assistant", Content: []types.Content{{Type: "thinking", Thinking: long}}},
		{Role: "assistant", Content: []types.Content{{Type: "thinking", Thinking: long + "more"}, {Type: "text", Text: "回答"}}},
		{Role: "assistant", Content: []types.Content{{Type: "thinking", Thinking: long + "more"}, {Type: "text", Text: "回答🙂"}, {Type: "toolCall", ID: "c", Name: "Bash", ArgumentsRaw: "{", Arguments: map[string]any{"nullable": nil}}}},
		{Role: "assistant", Content: []types.Content{{Type: "thinking", Thinking: long + "more"}, {Type: "text", Text: "回答🙂"}, {Type: "toolCall", ID: "c", Name: "Bash", ArgumentsRaw: `{"command":"echo hi"}`, Arguments: map[string]any{"command": "echo hi"}}}},
		{Role: "assistant", Content: []types.Content{{Type: "text", Text: "replacement"}}},
		{Role: "assistant", Content: nil, ErrorMessage: "aborted", StopReason: "error"},
		{Role: "assistant", Content: []types.Content{{Type: "text", Text: long}}},
		{Role: "assistant", Content: []types.Content{{Type: "text", Text: long + "recovered"}}},
	}
	patches := 0
	for i, message := range messages {
		ev := Event{Type: MessageUpdate, Seq: int64(i*7 + 10), Message: &message, AssistantMessageEvent: &AssistantDelta{Partial: message}}
		before, _ := json.Marshal(ev)
		wire := encoder.Encode(ev)
		if wire.AssistantMessageEvent != nil {
			t.Fatal("duplicate partial on wire")
		}
		if wire.MessagePatch != nil {
			patches++
		}
		encoded, _ := json.Marshal(wire)
		var received Event
		if err := json.Unmarshal(encoded, &received); err != nil {
			t.Fatal(err)
		}
		got, err := decoder.Decode(received)
		if err != nil {
			t.Fatal(err)
		}
		wantJSON, _ := json.Marshal(message)
		gotJSON, _ := json.Marshal(got.Message)
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("step %d: %s != %s", i, gotJSON, wantJSON)
		}
		after, _ := json.Marshal(ev)
		if string(before) != string(after) {
			t.Fatal("canonical event changed")
		}
	}
	if patches < 2 {
		t.Fatalf("only %d patches", patches)
	}
}

func TestMessageWireLinearBytesAndReconnect(t *testing.T) {
	measure := func(count int) int {
		var encoder MessageEncoder
		var decoder MessageDecoder
		bytes := 0
		for i := 1; i <= count; i++ {
			message := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: strings.Repeat("1234567890", i*10)}}}
			ev := Event{Type: MessageUpdate, Seq: int64(i * 2), Message: &message}
			wire := encoder.Encode(ev)
			raw, _ := json.Marshal(wire)
			bytes += len(raw)
			got, err := decoder.Decode(wire)
			if err != nil || !reflect.DeepEqual(got.Message, &message) {
				t.Fatalf("decode %d: %v", i, err)
			}
			if i == count/2 {
				// Slow replay / reconnect starts at an arbitrary global seq. It
				// must be independently decodable, with no earlier patch base.
				var freshEncoder MessageEncoder
				var freshDecoder MessageDecoder
				snapshot := freshEncoder.Encode(ev)
				if snapshot.Message == nil || snapshot.MessagePatch != nil {
					t.Fatal("resume needs snapshot")
				}
				if _, err := freshDecoder.Decode(snapshot); err != nil {
					t.Fatal(err)
				}
			}
		}
		return bytes
	}
	a, b := measure(100), measure(200)
	if b > a*22/10 {
		t.Fatalf("nonlinear wire growth: %d -> %d", a, b)
	}
	t.Logf("wire bytes 100/200 chunks: %d/%d", a, b)
}

func TestMessageWireRejectsMissingBaseAndResets(t *testing.T) {
	var encoder MessageEncoder
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: strings.Repeat("x", 1000)}}}
	first := encoder.Encode(Event{Type: MessageUpdate, Seq: 9, Message: &m})
	m.Content[0].Text += "more"
	patch := encoder.Encode(Event{Type: MessageUpdate, Seq: 17, Message: &m})
	var decoder MessageDecoder
	if _, err := decoder.Decode(patch); err == nil {
		t.Fatal("accepted missing base")
	}
	if _, err := decoder.Decode(first); err != nil {
		t.Fatal(err)
	}
	patch.MessageStream++
	if _, err := decoder.Decode(patch); err == nil {
		t.Fatal("accepted wrong message")
	}
	encoder.Encode(Event{Type: MessageEnd})
	if encoder.Encode(Event{Type: MessageUpdate, Seq: 40, Message: &m}).Message == nil {
		t.Fatal("new message needs snapshot")
	}
}
