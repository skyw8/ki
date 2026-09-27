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

// TestStreamPrinterPrintsLiveDeltas: a live stream's chunks are increments and
// each partial extends the last, so every chunk prints once, in arrival order.
func TestStreamPrinterPrintsLiveDeltas(t *testing.T) {
	steps := []struct{ chunk, text string }{{"Hel", "Hel"}, {"lo ", "Hello "}, {"world", "Hello world"}}
	var p streamPrinter
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.MessageStart, Message: assistant("")})
		for _, step := range steps {
			partial := *assistant(step.text)
			p.event(loop.Event{
				Type:                  loop.MessageUpdate,
				Message:               assistant(step.text),
				AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: step.chunk, Partial: partial},
			})
		}
	})
	if out != "Hello world" {
		t.Fatalf("stdout = %q", out)
	}
}

// TestStreamPrinterFillsSkippedChunks: a reader that fell behind the retention
// caps (or reconnected) sees a partial that moved on by several chunks at once.
// Printing the accumulated text's missing suffix is what keeps the output whole
// — the raw delta of the first update after the gap is only part of it.
func TestStreamPrinterFillsSkippedChunks(t *testing.T) {
	var p streamPrinter
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.MessageStart, Message: assistant("")})
		first := *assistant("Hel")
		p.event(loop.Event{
			Type:                  loop.MessageUpdate,
			Message:               assistant("Hel"),
			AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: "Hel", Partial: first},
		})
		// Three chunks' worth of text arrives as one update, with a delta that
		// only covers the newest one.
		later := *assistant("Hello world")
		p.event(loop.Event{
			Type:                  loop.MessageUpdate,
			Message:               assistant("Hello world"),
			AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: "world", Partial: later},
		})
	})
	if out != "Hello world" {
		t.Fatalf("stdout = %q", out)
	}
}

// TestStreamPrinterResyncsWhenThePartialIsNotExtended: providers that drop
// reasoning text from a later partial must not lose the printed suffix logic —
// the printer falls back to the whole accumulated text.
func TestStreamPrinterResyncsWhenThePartialIsNotExtended(t *testing.T) {
	var p streamPrinter
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.MessageStart, Message: assistant("")})
		first := *assistant("thinking first")
		p.event(loop.Event{
			Type:                  loop.MessageUpdate,
			Message:               assistant("thinking first"),
			AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: "thinking first", Partial: first},
		})
		switched := *assistant("answer")
		p.event(loop.Event{
			Type:                  loop.MessageUpdate,
			Message:               assistant("answer"),
			AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: "answer", Partial: switched},
		})
	})
	if out != "thinking firstanswer" {
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
