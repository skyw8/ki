package loop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	toolapi "ki/internal/tool"
	"ki/internal/tool/discovery"
	"ki/internal/types"
)

type modelExposureStreamer struct {
	requests []Request
	messages []types.Message
}

func (s *modelExposureStreamer) Stream(_ context.Context, req Request, _ func(AssistantDelta) error) (types.Message, error) {
	s.requests = append(s.requests, req)
	if len(s.messages) == 0 {
		return types.Message{Role: "assistant", StopReason: "stop", Content: []types.Content{{Type: "text", Text: "done"}}}, nil
	}
	m := s.messages[0]
	s.messages = s.messages[1:]
	return m, nil
}
func exposureCall(id, name string, args map[string]any) types.Message {
	return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{{Type: "toolCall", ID: id, Name: name, Arguments: args}}}
}
func exposedNames(specs []toolapi.Spec) []string {
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
	}
	return names
}

func TestModelToolsSearchLoadsNextRequestAndHeaders(t *testing.T) {
	hidden := namedTool{name: "hidden_document"}
	c := discovery.New([]toolapi.Tool{hidden})
	search := c.SearchTool()
	streamer := &modelExposureStreamer{messages: []types.Message{
		exposureCall("search", discovery.Name, map[string]any{"query": "hidden_document"}),
		exposureCall("hidden", hidden.Name(), map[string]any{}),
	}}
	var headers [][]string
	_, err := Run(t.Context(), "discover", nil, Config{Streamer: streamer, Tools: []toolapi.Tool{search, hidden}, ModelTools: func(history []types.Message) []toolapi.Tool {
		return append([]toolapi.Tool{search}, c.Loaded(history)...)
	}}, func(ev Event) error {
		if ev.Type == RequestHeader {
			headers = append(headers, exposedNames(ev.Tools))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, req := range streamer.requests {
		want := []string{discovery.Name}
		if i > 0 {
			want = append(want, hidden.Name())
		}
		if !slices.Equal(exposedNames(req.Tools), want) || !slices.Equal(headers[i], want) {
			t.Fatalf("request %d tools=%v header=%v want=%v", i, exposedNames(req.Tools), headers[i], want)
		}
	}
	if len(streamer.requests) != 3 {
		t.Fatalf("requests=%d", len(streamer.requests))
	}
}

func TestModelToolsKnownHiddenToolCanExecuteWithoutSearch(t *testing.T) {
	executed := false
	hidden := dispatchTestTool{namedTool: namedTool{name: "hidden_document"}, execute: func(context.Context, map[string]any) toolapi.Result {
		executed = true
		return toolapi.Result{Content: []types.Content{{Type: "text", Text: "known effect"}}}
	}}
	streamer := &modelExposureStreamer{messages: []types.Message{exposureCall("known", hidden.Name(), map[string]any{})}}
	_, err := Run(t.Context(), "known", nil, Config{Streamer: streamer, Tools: []toolapi.Tool{hidden}, ModelTools: func([]types.Message) []toolapi.Tool { return nil }}, nil)
	if err != nil || !executed {
		t.Fatalf("hidden capability revoked: executed=%v err=%v", executed, err)
	}
	for _, req := range streamer.requests {
		if len(req.Tools) != 0 {
			t.Fatal("hidden schema advertised")
		}
	}
	if !strings.Contains(streamer.requests[1].Messages[len(streamer.requests[1].Messages)-1].Text(), "known effect") {
		t.Fatal("hidden result lost")
	}
}

func TestModelToolsDiscoveryHonorsPolicyAndFinalContext(t *testing.T) {
	for _, mode := range []string{"before", "after", "afterError", "afterRedacted", "transform"} {
		t.Run(mode, func(t *testing.T) {
			hidden := namedTool{name: "hidden_document"}
			c := discovery.New([]toolapi.Tool{hidden})
			search := c.SearchTool()
			streamer := &modelExposureStreamer{messages: []types.Message{exposureCall("search", discovery.Name, map[string]any{"query": "hidden_document"})}}
			hooks := Hooks{}
			switch mode {
			case "before":
				hooks.BeforeTool = func(_ context.Context, _ string, args map[string]any) (map[string]any, bool, string, bool, error) {
					return args, true, "blocked", false, nil
				}
			case "after":
				hooks.AfterTool = func(_ context.Context, _ string, _ map[string]any, res toolapi.Result) (toolapi.Result, error) {
					res.IsError = true
					return res, nil
				}
			case "afterError":
				hooks.AfterTool = func(_ context.Context, _ string, _ map[string]any, res toolapi.Result) (toolapi.Result, error) {
					return res, errors.New("output policy failed")
				}
			case "afterRedacted":
				hooks.AfterTool = func(_ context.Context, _ string, _ map[string]any, res toolapi.Result) (toolapi.Result, error) {
					res.Content = []types.Content{{Type: "text", Text: "safe redacted output"}}
					return res, nil
				}
			case "transform":
				hooks.TransformContext = func(_ context.Context, msgs []types.Message) ([]types.Message, error) {
					return slices.DeleteFunc(slices.Clone(msgs), func(m types.Message) bool { return m.Role == "toolResult" }), nil
				}
			}
			_, err := Run(t.Context(), "discover", nil, Config{Streamer: streamer, Tools: []toolapi.Tool{search, hidden}, Hooks: hooks, ModelTools: func(history []types.Message) []toolapi.Tool {
				return append([]toolapi.Tool{search}, c.Loaded(history)...)
			}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, req := range streamer.requests {
				if !slices.Equal(exposedNames(req.Tools), []string{discovery.Name}) {
					t.Fatalf("%s leaked hidden tools: %v", mode, req.Tools)
				}
			}
			if mode == "afterError" {
				for _, message := range streamer.requests[1].Messages {
					if message.Role == "toolResult" && (!message.IsError || strings.Contains(message.Text(), `"tools"`) || strings.Contains(message.Text(), `"hidden_document"`)) {
						t.Fatalf("failed output policy exposed search schema: %+v", message)
					}
				}
			}
		})
	}
}

func TestModelToolsRejectsUnknownAndDeduplicatesCanonicalNames(t *testing.T) {
	known := namedTool{name: "read"}
	registry, err := toolapi.NewRegistry([]toolapi.Tool{known})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := modelToolSpecs(Config{Tools: []toolapi.Tool{known}, ModelTools: func([]types.Message) []toolapi.Tool { return []toolapi.Tool{known, namedTool{name: "Read"}} }}, registry, nil)
	if err != nil || !slices.Equal(exposedNames(specs), []string{"read"}) {
		t.Fatalf("dedupe %v err=%v", specs, err)
	}
	for _, tools := range [][]toolapi.Tool{{namedTool{name: "unknown"}}, {nil}, {namedTool{name: "invalid-tool"}}} {
		if _, err := modelToolSpecs(Config{ModelTools: func([]types.Message) []toolapi.Tool { return tools }}, registry, nil); err == nil {
			t.Fatalf("accepted arbitrary advertised tool %v", tools)
		}
	}
	streamer := &modelExposureStreamer{}
	_, err = Run(t.Context(), "unknown", nil, Config{Streamer: streamer, Tools: []toolapi.Tool{known}, ModelTools: func([]types.Message) []toolapi.Tool { return []toolapi.Tool{namedTool{name: "unknown"}} }}, nil)
	if err == nil || len(streamer.requests) != 0 {
		t.Fatalf("unknown schema reached provider: %v", err)
	}
}
