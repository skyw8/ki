package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/types"
)

func TestScriptedHoldWaitsForCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &Scripted{}
	req := loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: HoldToken}}}}}
	done := make(chan error, 1)
	go func() {
		_, err := s.Stream(ctx, req, func(loop.AssistantDelta) error { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("hold returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hold did not unblock")
	}
}

func TestScriptedWriteEnvOnlyOnLatestUser(t *testing.T) {
	s := &Scripted{}
	first, err := s.Stream(context.Background(), loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: WriteEnvToken}}}}}, func(loop.AssistantDelta) error { return nil })
	if err != nil || len(first.ToolCalls()) != 1 || first.ToolCalls()[0].Name != "write" {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := s.Stream(context.Background(), loop.Request{Messages: []types.Message{
		{Role: "user", Content: []types.Content{{Type: "text", Text: WriteEnvToken}}},
		first,
		{Role: "toolResult", ToolCallID: "call-write-env", ToolName: "write", Content: []types.Content{{Type: "text", Text: "blocked"}}},
	}}, func(loop.AssistantDelta) error { return nil })
	if err != nil || second.Text() != "ok" {
		t.Fatalf("second should stop looping: %+v %v", second, err)
	}
}

func TestScriptedMarkdownFixture(t *testing.T) {
	s := &Scripted{}
	msg, err := s.Stream(context.Background(), loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: MarkdownToken}}}}}, func(loop.AssistantDelta) error { return nil })
	if err != nil || msg.Text() != MarkdownFixture {
		t.Fatalf("got %+v %v", msg, err)
	}
}

func TestScriptedDelayCompletesAfterDuration(t *testing.T) {
	s := &Scripted{}
	req := loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "e2e-delay-60"}}}}}
	start := time.Now()
	msg, err := s.Stream(context.Background(), req, func(loop.AssistantDelta) error { return nil })
	if err != nil || msg.Text() != "ok" {
		t.Fatalf("got %+v %v", msg, err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("delay not applied: %v", elapsed)
	}
}

func TestScriptedDelayUnblocksOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &Scripted{}
	req := loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "e2e-delay-5000"}}}}}
	done := make(chan error, 1)
	go func() {
		_, err := s.Stream(ctx, req, func(loop.AssistantDelta) error { return nil })
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delay did not unblock on cancel")
	}
}

func TestScriptedSkipsHoldWithoutToken(t *testing.T) {
	s := &Scripted{}
	msg, err := s.Stream(context.Background(), loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hello"}}}}}, func(loop.AssistantDelta) error { return nil })
	if err != nil || msg.Text() != "ok" {
		t.Fatalf("got %+v %v", msg, err)
	}
}

func TestScriptedStreamsGrowingChunks(t *testing.T) {
	s := &Scripted{}
	req := loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "e2e-stream-8"}}}}}
	var partials []types.Message
	deltas := 0
	msg, err := s.Stream(context.Background(), req, func(d loop.AssistantDelta) error {
		deltas++
		partials = append(partials, d.Partial)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if deltas != 8 {
		t.Fatalf("deltas = %d, want 8", deltas)
	}
	// Every partial carries the whole accumulated text, like the real adapters.
	if got, want := partials[7].Text(), msg.Text(); got != want {
		t.Fatalf("last partial %q, final %q", got, want)
	}
	if len(partials[0].Text()) >= len(partials[7].Text()) {
		t.Fatal("partials must grow")
	}
}

func TestScriptedStreamTokenIgnoresMalformed(t *testing.T) {
	s := &Scripted{}
	req := loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "e2e-stream-x"}}}}}
	msg, err := s.Stream(context.Background(), req, func(loop.AssistantDelta) error { return nil })
	if err != nil || msg.Text() != "ok" {
		t.Fatalf("got %q %v", msg.Text(), err)
	}
}

func TestScriptedStreamsParagraphBlocks(t *testing.T) {
	s := &Scripted{}
	req := loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "e2e-blocks-12"}}}}}
	var last types.Message
	deltas := 0
	msg, err := s.Stream(context.Background(), req, func(d loop.AssistantDelta) error {
		deltas++
		last = d.Partial
		return nil
	})
	if err != nil || deltas != 12 {
		t.Fatalf("deltas = %d, err %v", deltas, err)
	}
	if got := strings.Count(msg.Text(), "\n\n"); got != 12 {
		t.Fatalf("blank-line separated blocks = %d, want 12", got)
	}
	if last.Text() != msg.Text() {
		t.Fatal("the last partial must carry the whole text")
	}
}
