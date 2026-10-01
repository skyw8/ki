package loop

import (
	"context"
	"testing"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

type namedTool struct {
	oneTool
	name string
}

func (t namedTool) Name() string { return t.name }

func TestRegistryAndProtocolUseCanonicalSchemasAndRequestedResultPairing(t *testing.T) {
	tool := namedTool{name: "read"}
	registry, err := toolapi.NewRegistry([]toolapi.Tool{tool})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read", "Read"} {
		if resolved, ok := registry.Lookup(name); !ok || resolved.Name() != "read" {
			t.Fatalf("alias %s: %v %v", name, resolved, ok)
		}
	}
	if _, ok := registry.Lookup("READ"); ok {
		t.Fatal("arbitrary case accepted")
	}
	if _, err := toolapi.NewRegistry([]toolapi.Tool{tool, namedTool{name: "Read"}}); err == nil {
		t.Fatal("canonical collision allowed")
	}
	streamer := &scripted{}
	var hook string
	var result *types.Message
	var started Event
	_, err = Run(t.Context(), "read it", nil, Config{Streamer: streamer, Tools: []toolapi.Tool{tool}, Hooks: Hooks{BeforeTool: func(_ context.Context, name string, args map[string]any) (map[string]any, bool, string, bool, error) {
		hook = name
		return args, false, "", false, nil
	}}}, func(ev Event) error {
		if ev.Type == RequestHeader && (len(ev.Tools) != 1 || ev.Tools[0].Name != "read") {
			t.Fatalf("duplicate/noncanonical schema: %+v", ev.Tools)
		}
		if ev.Type == ToolExecutionStart {
			started = ev
		}
		if ev.Type == MessageEnd && ev.Message != nil && ev.Message.Role == "toolResult" {
			result = ev.Message
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hook != "read" || started.ToolName != "read" || started.RequestedToolName != "Read" || result == nil || result.ToolName != "Read" {
		t.Fatalf("hook=%s event=%+v result=%+v", hook, started, result)
	}
}
