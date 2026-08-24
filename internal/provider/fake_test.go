package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/types"
)

func TestScriptedHoldWaitsForCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
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

func TestScriptedSkipsHoldWithoutToken(t *testing.T) {
	s := &Scripted{}
	msg, err := s.Stream(context.Background(), loop.Request{Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hello"}}}}}, func(loop.AssistantDelta) error { return nil })
	if err != nil || msg.Text() != "ok" {
		t.Fatalf("got %+v %v", msg, err)
	}
}
