package loop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

type turnIdentityStreamer struct {
	scripted
	requests []Request
}

func (s *turnIdentityStreamer) Stream(ctx context.Context, req Request, emit func(AssistantDelta) error) (types.Message, error) {
	s.requests = append(s.requests, req)
	return s.scripted.Stream(ctx, req, emit)
}

func TestLogicalTurnIdentitySurvivesToolContinuations(t *testing.T) {
	var previous string
	for range 2 {
		streamer := &turnIdentityStreamer{}
		_, err := Run(context.Background(), "read", nil, Config{
			SessionID: "session", Streamer: streamer, Tools: []toolapi.Tool{oneTool{}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(streamer.requests) != 2 {
			t.Fatalf("requests=%d, want tool call and continuation", len(streamer.requests))
		}
		id := streamer.requests[0].TurnID
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("logical turn id %q is not a UUID: %v", id, err)
		}
		if id == previous || streamer.requests[1].TurnID != id {
			t.Fatalf("turn identity changed within continuation or reused across prompts: %+v", streamer.requests)
		}
		previous = id
		raw, err := json.Marshal(streamer.requests[0])
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil || wire["turnId"] != id {
			t.Fatalf("provider RPC lost turn identity: %s (%v)", raw, err)
		}
	}
}

func TestLogicalTurnIdentityCanBeProvided(t *testing.T) {
	streamer := &turnIdentityStreamer{}
	_, err := Run(context.Background(), "read", nil, Config{
		SessionID: "session", TurnID: "known-turn", Streamer: streamer, Tools: []toolapi.Tool{oneTool{}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range streamer.requests {
		if request.TurnID != "known-turn" {
			t.Fatalf("provided turn identity replaced: %q", request.TurnID)
		}
	}
}
