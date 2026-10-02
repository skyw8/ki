package loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ki/internal/telemetry"
	toolapi "ki/internal/tool"
	"ki/internal/tool/output"
	"ki/internal/types"
)

type dispatchTestTool struct {
	namedTool
	validate func(map[string]any) error
	execute  func(context.Context, map[string]any) toolapi.Result
}

func (t dispatchTestTool) Validate(args map[string]any) error {
	if t.validate != nil {
		return t.validate(args)
	}
	return nil
}

func (t dispatchTestTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	if t.execute != nil {
		return t.execute(ctx, args)
	}
	return t.namedTool.Execute(ctx, args)
}

func TestNestedDispatcherHooksAliasIdentityAndFullIntermediate(t *testing.T) {
	store, err := output.NewWithConfig(output.Config{Root: t.TempDir(), PreviewBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fullText := strings.Repeat("full-result\n", 100)
	fullDetails := strings.Repeat("details", 1<<14)
	var identity toolapi.ExecutionIdentity
	var stages []string
	tool := dispatchTestTool{
		namedTool: namedTool{name: "read"},
		validate: func(args map[string]any) error {
			stages = append(stages, "validate")
			if args["file_path"] != "a" {
				return errors.New("bad args")
			}
			return nil
		},
		execute: func(ctx context.Context, args map[string]any) toolapi.Result {
			stages = append(stages, "execute")
			identity = toolapi.ExecutionFromContext(ctx)
			if args["file_path"] != "rewritten" {
				t.Errorf("hook rewrite lost: %#v", args)
			}
			return toolapi.Result{Content: []types.Content{{Type: "text", Text: "pre-hook"}}}
		},
	}
	var events []Event
	allowed := []toolapi.Tool{tool}
	d, err := NewToolDispatcher(Config{
		RunID: "run", AgentID: "agent", Generation: 4, SessionID: "session",
		Tools: allowed, OutputStore: store,
		Hooks: Hooks{
			BeforeTool: func(_ context.Context, name string, args map[string]any) (map[string]any, bool, string, bool, error) {
				stages = append(stages, "before")
				if name != "read" {
					t.Errorf("noncanonical hook name %q", name)
				}
				return map[string]any{"file_path": "rewritten"}, false, "", true, nil
			},
			AfterTool: func(_ context.Context, name string, args map[string]any, res toolapi.Result) (toolapi.Result, error) {
				stages = append(stages, "after")
				res.Content[0].Text = fullText
				res.Details = map[string]any{"blob": fullDetails}
				return res, nil
			},
		},
	}, func(ev Event) error { events = append(events, ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Replacing the caller's backing slice must not change the capability set.
	allowed[0] = namedTool{name: "other"}
	res, err := d.DispatchNested(t.Context(), NestedToolCall{
		ParentCallID: "outer", CellID: "cell", Name: "Read", Arguments: map[string]any{"file_path": "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stages, []string{"validate", "before", "execute", "after"}) {
		t.Fatalf("policy order %v", stages)
	}
	if res.Content[0].Text != fullText || res.Details.(map[string]any)["blob"] != fullDetails || !res.Terminate {
		t.Fatalf("intermediate lost content/details/terminate: %+v", res)
	}
	if identity.RunID != "run" || identity.AgentID != "agent" || identity.Generation != 4 || identity.CallID == "" || identity.CallID == "outer" {
		t.Fatalf("untrusted/lost identity: %+v", identity)
	}
	if len(events) != 2 {
		t.Fatalf("unexpected transcript events: %+v", events)
	}
	for _, ev := range events {
		if ev.ParentCallID != "outer" || ev.CellID != "cell" || ev.ToolCallID != identity.CallID ||
			ev.ToolName != "read" || ev.RequestedToolName != "Read" || ev.Message != nil {
			t.Fatalf("audit attribution: %+v", ev)
		}
	}
	end := events[1].Result.(map[string]any)
	details := end["Details"].(map[string]any)
	if details["truncated"] != true {
		t.Fatalf("unbounded details: %#v", details)
	}
	encoded, err := json.Marshal(events[1])
	if err != nil || len(encoded) >= 64<<10 || strings.Contains(string(encoded), fullText) {
		t.Fatalf("audit is not separately bounded: bytes=%d err=%v", len(encoded), err)
	}
}

func TestNestedDispatcherRejectionAndRecursion(t *testing.T) {
	var called, before bool
	read := dispatchTestTool{
		namedTool: namedTool{name: "read"},
		validate:  func(map[string]any) error { return errors.New("required file_path") },
		execute:   func(context.Context, map[string]any) toolapi.Result { called = true; return toolapi.Result{} },
	}
	var events []Event
	d, err := NewToolDispatcher(Config{
		Tools: []toolapi.Tool{read, namedTool{name: "exec"}, namedTool{name: "wait"}},
		Hooks: Hooks{BeforeTool: func(_ context.Context, _ string, args map[string]any) (map[string]any, bool, string, bool, error) {
			before = true
			return args, false, "", false, nil
		}},
	}, func(ev Event) error { events = append(events, ev); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Read", "Exec", "Wait", "disabled"} {
		res, err := d.DispatchNested(t.Context(), NestedToolCall{ParentCallID: "outer", CellID: "cell", Name: name})
		if err != nil || !res.IsError {
			t.Fatalf("%s: %+v, %v", name, res, err)
		}
	}
	if called || before {
		t.Fatal("rejected request reached hook or execution")
	}
	if events[0].ToolName != "read" || events[0].RequestedToolName != "Read" {
		t.Fatalf("rejection lost canonical name: %+v", events[0])
	}
	if events[1].Result == nil {
		t.Fatal("rejected nested call omitted durable outcome")
	}
}

func TestNestedDispatcherFreeformAndNotification(t *testing.T) {
	tool := &rawTool{}
	var events []Event
	d, err := NewToolDispatcher(Config{Tools: []toolapi.Tool{tool}}, func(ev Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := "*** Begin Patch\n*** End Patch"
	if _, err := d.DispatchNested(t.Context(), NestedToolCall{ParentCallID: "outer", CellID: "cell", Name: "ApplyPatch", Input: &raw}); err != nil {
		t.Fatal(err)
	}
	if tool.got != raw {
		t.Fatalf("freeform input %q", tool.got)
	}
	if err := d.NotifyNested("outer", "cell", strings.Repeat("notice", 1<<14)); err != nil {
		t.Fatal(err)
	}
	notice := events[len(events)-1]
	if notice.Type != ToolExecutionUpdate || notice.ParentCallID != "outer" || notice.CellID != "cell" ||
		notice.PartialResult.(map[string]any)["truncated"] != true || notice.Message != nil {
		t.Fatalf("notification is not a bounded attributed event: %+v", notice)
	}
}

func TestNestedDispatcherPersistenceFailureStopsEffects(t *testing.T) {
	persistErr := errors.New("disk failure")
	called := false
	tool := dispatchTestTool{namedTool: namedTool{name: "read"}, execute: func(context.Context, map[string]any) toolapi.Result {
		called = true
		return toolapi.Result{}
	}}
	d, err := NewToolDispatcher(Config{Tools: []toolapi.Tool{tool}}, func(Event) error { return persistErr })
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.DispatchNested(t.Context(), NestedToolCall{ParentCallID: "outer", CellID: "cell", Name: "read"})
	if !errors.Is(err, persistErr) || called {
		t.Fatalf("unrecorded effects: called=%v err=%v", called, err)
	}
}

func TestNestedDispatcherAfterHookFailureDoesNotExposeResult(t *testing.T) {
	hookErr := errors.New("output policy unavailable")
	tool := dispatchTestTool{namedTool: namedTool{name: "read"}, execute: func(context.Context, map[string]any) toolapi.Result {
		return toolapi.Result{Content: []types.Content{{Type: "text", Text: "sensitive"}}, Terminate: true}
	}}
	d, err := NewToolDispatcher(Config{
		Tools: []toolapi.Tool{tool},
		Hooks: Hooks{AfterTool: func(context.Context, string, map[string]any, toolapi.Result) (toolapi.Result, error) {
			return toolapi.Result{}, hookErr
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.DispatchNested(t.Context(), NestedToolCall{ParentCallID: "outer", CellID: "cell", Name: "read"})
	if !errors.Is(err, hookErr) || !res.IsError || !res.Terminate || strings.Contains(res.Content[0].Text, "sensitive") {
		t.Fatalf("failed output policy leaked intermediate: %+v, %v", res, err)
	}
}

type progressDispatchTool struct {
	dispatchTestTool
	run func(context.Context, func(any)) toolapi.Result
}

func (t progressDispatchTool) ExecuteWithProgress(ctx context.Context, _ map[string]any, emit func(any)) toolapi.Result {
	return t.run(ctx, emit)
}

func TestNestedDispatcherProgressPersistenceFailureCancelsExecution(t *testing.T) {
	persistErr := errors.New("progress disk failure")
	tool := progressDispatchTool{
		dispatchTestTool: dispatchTestTool{namedTool: namedTool{name: "read"}},
		run: func(ctx context.Context, emit func(any)) toolapi.Result {
			emit("progress")
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("failed progress persistence did not cancel tool context")
			}
			return toolapi.Result{IsError: true}
		},
	}
	d, err := NewToolDispatcher(Config{Tools: []toolapi.Tool{tool}}, func(ev Event) error {
		if ev.Type == ToolExecutionUpdate {
			return persistErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.DispatchNested(t.Context(), NestedToolCall{ParentCallID: "outer", CellID: "cell", Name: "Read"})
	if !errors.Is(err, persistErr) {
		t.Fatalf("progress persistence failure swallowed: %v", err)
	}
}

func TestNestedDispatcherEndPersistenceFailurePropagates(t *testing.T) {
	persistErr := errors.New("end disk failure")
	d, err := NewToolDispatcher(Config{Tools: []toolapi.Tool{namedTool{name: "read"}}}, func(ev Event) error {
		if ev.Type == ToolExecutionEnd {
			return persistErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.DispatchNested(t.Context(), NestedToolCall{ParentCallID: "outer", CellID: "cell", Name: "read"})
	if !errors.Is(err, persistErr) {
		t.Fatalf("end persistence failure swallowed: %v", err)
	}
}

func TestNestedDispatcherBlockedTerminatePropagates(t *testing.T) {
	called := false
	tool := dispatchTestTool{namedTool: namedTool{name: "read"}, execute: func(context.Context, map[string]any) toolapi.Result {
		called = true
		return toolapi.Result{}
	}}
	d, err := NewToolDispatcher(Config{
		Tools: []toolapi.Tool{tool},
		Hooks: Hooks{BeforeTool: func(_ context.Context, _ string, args map[string]any) (map[string]any, bool, string, bool, error) {
			return args, true, "blocked", true, nil
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.DispatchNested(t.Context(), NestedToolCall{ParentCallID: "outer", CellID: "cell", Name: "read"})
	if err != nil || called || !res.IsError || !res.Terminate {
		t.Fatalf("blocked terminate lost: %+v, called=%v err=%v", res, called, err)
	}
}

func TestNestedAuditPreservesLargeIntegerArguments(t *testing.T) {
	const value uint64 = 1<<63 + 7
	ev, err := BoundNestedToolEvent(Event{ParentCallID: "outer", Args: map[string]any{"value": value}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ev.Args)
	if err != nil || string(raw) != `{"value":9223372036854775815}` {
		t.Fatalf("integer rounded by audit snapshot: %s, %v", raw, err)
	}
}

func TestOrdinaryInvalidRegistryRetainsPerCallRejectionRecords(t *testing.T) {
	dir := t.TempDir()
	recorder := telemetry.NewRun(dir, "session", "run")
	t.Cleanup(func() { recorder.Close(); telemetry.Forget(dir) })
	called, hooked := false, false
	tool := dispatchTestTool{namedTool: namedTool{name: "read"}, execute: func(context.Context, map[string]any) toolapi.Result {
		called = true
		return toolapi.Result{}
	}}
	var events []Event
	messages, terminate := executeTools(t.Context(), Config{
		Telemetry: recorder, Tools: []toolapi.Tool{tool, namedTool{name: "Read"}},
		Hooks: Hooks{BeforeTool: func(_ context.Context, _ string, args map[string]any) (map[string]any, bool, string, bool, error) {
			hooked = true
			return args, false, "", false, nil
		}},
	}, []types.Content{{ID: "one", Name: "Read"}, {ID: "two", Name: "read"}}, func(ev Event) error {
		events = append(events, ev)
		return nil
	})
	if called || hooked || terminate || len(messages) != 2 || len(events) != 4 {
		t.Fatalf("invalid registry policy lost: called=%v hooked=%v terminate=%v messages=%+v events=%+v", called, hooked, terminate, messages, events)
	}
	for i, message := range messages {
		if !message.IsError || message.ToolCallID != []string{"one", "two"}[i] || !strings.HasPrefix(message.Content[0].Text, "unknown tool ") {
			t.Fatalf("missing per-call rejection: %+v", message)
		}
		if events[i*2].Type != ToolExecutionStart || events[i*2+1].Type != ToolExecutionEnd || !events[i*2+1].IsError {
			t.Fatalf("missing per-call events: %+v", events)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, telemetry.FileName))
	if err != nil || strings.Count(string(raw), `"unknown_tool"`) != 2 || strings.Count(string(raw), `"rejected"`) != 2 {
		t.Fatalf("missing per-call telemetry: %s, %v", raw, err)
	}
}
