package cli

import (
	"io"
	"os"
	"testing"

	"ki/internal/loop"
	"ki/internal/types"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assistant(text string) *types.Message {
	return &types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: text}}}
}

// TestStreamPrinterPrintsReplayedPartialOnce: the server trims superseded chunks
// from the replay, so a client attaching mid-message sees one update carrying
// the whole accumulated text. Printing the raw delta would drop the prefix.
func TestStreamPrinterPrintsReplayedPartialOnce(t *testing.T) {
	var p streamPrinter
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.MessageStart, Message: assistant("")})
		partial := *assistant("hello world")
		p.event(loop.Event{
			Type:                  loop.MessageUpdate,
			Message:               assistant("hello world"),
			AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: "d", Partial: partial},
		})
	})
	if out != "hello world" {
		t.Fatalf("stdout = %q", out)
	}
}

// TestStreamPrinterPrintsLiveDeltas: a live stream's chunks are increments, so
// each one prints as-is in arrival order.
func TestStreamPrinterPrintsLiveDeltas(t *testing.T) {
	var p streamPrinter
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.MessageStart, Message: assistant("")})
		for i, chunk := range []string{"Hel", "lo ", "world"} {
			text := "Hello world"[:3+i*3]
			partial := *assistant(text)
			p.event(loop.Event{
				Type:                  loop.MessageUpdate,
				Message:               assistant(text),
				AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: chunk, Partial: partial},
			})
		}
	})
	if out != "Hello world" {
		t.Fatalf("stdout = %q", out)
	}
}

// TestStreamPrinterPrintsAReplayedMessageEnd: a run can finish before the CLI
// connects, leaving only the message_end in the replay.
func TestStreamPrinterPrintsAReplayedMessageEnd(t *testing.T) {
	var p streamPrinter
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.MessageStart, Message: assistant("")})
		p.event(loop.Event{Type: loop.MessageEnd, Message: assistant("done"), EntryID: "e1"})
	})
	if out != "done" {
		t.Fatalf("stdout = %q", out)
	}
}

// TestStreamPrinterDoesNotRepeatAFinishedMessage: a live run prints through its
// chunks, so the message_end that follows must print nothing extra.
func TestStreamPrinterDoesNotRepeatAFinishedMessage(t *testing.T) {
	var p streamPrinter
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.MessageStart, Message: assistant("")})
		text := "done"
		partial := *assistant(text)
		p.event(loop.Event{
			Type:                  loop.MessageUpdate,
			Message:               assistant(text),
			AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: "done", Partial: partial},
		})
		p.event(loop.Event{Type: loop.MessageEnd, Message: assistant("done"), EntryID: "e1"})
	})
	if out != "done" {
		t.Fatalf("stdout = %q", out)
	}
}
