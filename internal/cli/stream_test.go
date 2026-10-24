package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

func TestStreamPrinterShowsAsyncQuestionsWithoutWaitingOrRepeatingUpdates(t *testing.T) {
	var p streamPrinter
	question := session.Question{QuestionItemID: "q1", Index: 0, Title: "发布范围？", Options: []string{"内部", "公开"}}
	out := captureStdout(t, func() {
		p.event(loop.Event{Type: loop.QuestionRequested, Questions: []session.Question{question, {QuestionItemID: "q2", Index: 1, Title: "何时发布？"}}})
		p.event(loop.Event{Type: loop.QuestionUpdated, Question: &question})
		p.event(loop.Event{Type: loop.MessageEnd, Message: assistant("Continuing independent work.")})
	})
	for _, text := range []string{"[question 1] 发布范围？", "1. 内部", "2. 公开", "[question 2] 何时发布？", "not paused", "No option is submitted automatically", "Continuing independent work."} {
		if !strings.Contains(out, text) {
			t.Fatalf("missing %q in %q", text, out)
		}
	}
	if strings.Count(out, "发布范围？") != 1 {
		t.Fatalf("question repeated on update: %q", out)
	}
}

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
