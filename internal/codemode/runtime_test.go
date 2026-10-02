package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"ki/internal/types"
)

func testSession(t *testing.T, limits Limits) *Session {
	t.Helper()
	s := NewLocalSession(limits)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	})
	return s
}

func executeTest(t *testing.T, s *Session, source string, callbacks Callbacks, tools ...ToolDefinition) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := s.Execute(ctx, ExecuteRequest{ParentCallID: t.Name(), Source: source, Tools: tools, YieldTime: time.Second}, callbacks)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func textOutput(resp Response) string {
	var out strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	return out.String()
}

func TestFreshVMAndExplicitStore(t *testing.T) {
	s := testSession(t, Limits{})
	resp := executeTest(t, s, `globalThis.privateValue=123; store("x",{n:1}); text(load("x")); 42;`, Callbacks{})
	if resp.Status != "completed" || textOutput(resp) != `{"n":1}` {
		t.Fatalf("%+v", resp)
	}
	resp = executeTest(t, s, `text(typeof privateValue); const x=load("x"); x.n=2; text(load("x"));`, Callbacks{})
	if resp.Status != "completed" || textOutput(resp) != `undefined{"n":1}` {
		t.Fatalf("%+v", resp)
	}
	resp = executeTest(t, s, `store("errorWrite",7); throw new Error("expected");`, Callbacks{})
	if resp.Status != "failed" || !strings.Contains(resp.Error, "expected") {
		t.Fatalf("%+v", resp)
	}
	resp = executeTest(t, s, `text(load("errorWrite"));`, Callbacks{})
	if textOutput(resp) != "7" {
		t.Fatalf("exception did not commit store: %+v", resp)
	}
}

func TestPromiseToolsAndFreeform(t *testing.T) {
	s := testSession(t, Limits{})
	var mu sync.Mutex
	var invocations []Invocation
	cb := Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
		mu.Lock()
		invocations = append(invocations, inv)
		mu.Unlock()
		return ToolResult{Content: []types.Content{{Type: "text", Text: inv.Name}}, Details: map[string]any{"n": 3}}, nil
	}}
	resp := executeTest(t, s, `
const results=await Promise.all([tools.read({n:1}),tools.patch("raw patch")]);
text(results.map(r=>r.content[0].text).join(","));
text(results[0].details.n);
text(ALL_TOOLS.map(t=>Object.keys(t).sort().join(",")).join(";"));
`, cb, ToolDefinition{Name: "read", Description: "read"}, ToolDefinition{Name: "patch", Freeform: true})
	if resp.Status != "completed" || textOutput(resp) != "read,patch3description,name;description,name" {
		t.Fatalf("%+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(invocations) != 2 {
		t.Fatalf("%+v", invocations)
	}
	for _, inv := range invocations {
		if inv.ParentCallID != t.Name() || inv.CellID != resp.CellID || !strings.HasPrefix(inv.ToolCallID, resp.CellID+"/tool-") {
			t.Fatalf("unattributed callback: %+v", inv)
		}
		if inv.Name == "patch" && inv.Input != "raw patch" {
			t.Fatalf("%+v", inv)
		}
		if inv.Name == "read" && inv.Arguments["n"] != float64(1) {
			t.Fatalf("%+v", inv)
		}
	}
}

func TestYieldWaitSnapshotsAndCancellation(t *testing.T) {
	s := testSession(t, Limits{})
	executeTest(t, s, `store("x","old");`, Callbacks{})
	gate := make(chan struct{})
	started := make(chan struct{})
	cb := Callbacks{Invoke: func(ctx context.Context, _ Invocation) (ToolResult, error) {
		close(started)
		select {
		case <-gate:
			return ToolResult{}, nil
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := s.Execute(ctx, ExecuteRequest{ParentCallID: "first", Source: `store("local",1);text("early");yield_control();await tools.gate({});text(load("x"));store("late",2);`, Tools: []ToolDefinition{{Name: "gate"}}, YieldTime: time.Second}, cb)
	if err != nil || !resp.Running() || textOutput(resp) != "early" {
		t.Fatalf("%+v %v", resp, err)
	}
	<-started
	if out := textOutput(executeTest(t, s, `text(load("local"));store("x","new");`, Callbacks{})); out != "undefined" {
		t.Fatalf("yield committed writes: %s", out)
	}
	close(gate)
	final, err := s.Wait(ctx, WaitRequest{CellID: resp.CellID, YieldTime: time.Second})
	if err != nil || final.Status != "completed" || textOutput(final) != "old" {
		t.Fatalf("%+v %v", final, err)
	}
	out := executeTest(t, s, `text(load("late"));text(load("x"));`, Callbacks{})
	if textOutput(out) != "2new" {
		t.Fatalf("%+v", out)
	}
	if _, err = s.Wait(ctx, WaitRequest{CellID: resp.CellID}); err == nil {
		t.Fatal("closed cell accepted")
	}
}

func TestTerminateDiscardsWritesAndDrainsTools(t *testing.T) {
	s := testSession(t, Limits{})
	started := make(chan struct{})
	drained := make(chan struct{})
	cb := Callbacks{Invoke: func(ctx context.Context, _ Invocation) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(drained)
		return ToolResult{}, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := s.Execute(ctx, ExecuteRequest{ParentCallID: "abort", Source: `store("aborted",1);yield_control();await tools.block({});`, Tools: []ToolDefinition{{Name: "block"}}, YieldTime: time.Second}, cb)
	if err != nil || !resp.Running() {
		t.Fatalf("%+v %v", resp, err)
	}
	<-started
	final, err := s.Wait(ctx, WaitRequest{CellID: resp.CellID, Terminate: true, YieldTime: time.Second})
	if err != nil || final.Status != "terminated" {
		t.Fatalf("%+v %v", final, err)
	}
	select {
	case <-drained:
	default:
		t.Fatal("terminate published before callback drained")
	}
	if got := textOutput(executeTest(t, s, `text(load("aborted"));`, Callbacks{})); got != "undefined" {
		t.Fatal(got)
	}
}

func TestUnawaitedToolsCancelBeforeCompletion(t *testing.T) {
	s := testSession(t, Limits{})
	drained := make(chan struct{})
	started := make(chan struct{})
	cb := Callbacks{Invoke: func(ctx context.Context, _ Invocation) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(drained)
		return ToolResult{}, ctx.Err()
	}}
	resp := executeTest(t, s, `tools.block({}); await tools.started({}); text("done");`, Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
		if inv.Name == "started" {
			select {
			case <-started:
				return ToolResult{}, nil
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
		}
		return cb.Invoke(ctx, inv)
	}}, ToolDefinition{Name: "block"}, ToolDefinition{Name: "started"})
	if resp.Status != "completed" || textOutput(resp) != "done" {
		t.Fatalf("%+v", resp)
	}
	select {
	case <-drained:
	default:
		t.Fatal("unawaited callback was not drained")
	}
}

func TestTimerAndNotify(t *testing.T) {
	s := testSession(t, Limits{})
	notification := make(chan Notification, 1)
	resp := executeTest(t, s, `
const id=setTimeout(()=>text("wrong"),0);clearTimeout(id);
await new Promise(resolve=>setTimeout(resolve,0));
notify("progress");text("done");
`, Callbacks{Notify: func(ctx context.Context, n Notification) error { notification <- n; return nil }})
	if resp.Status != "completed" || textOutput(resp) != "progressdone" {
		t.Fatalf("%+v", resp)
	}
	select {
	case n := <-notification:
		if n.ParentCallID != t.Name() || n.CellID != resp.CellID || n.Text != "progress" {
			t.Fatalf("%+v", n)
		}
	default:
		t.Fatal("notification not drained")
	}
}

func TestExitIsUncatchableSuccess(t *testing.T) {
	s := testSession(t, Limits{})
	resp := executeTest(t, s, `store("x",1);try{exit();}catch(e){text("caught");}text("wrong");`, Callbacks{})
	if resp.Status != "completed" || len(resp.Content) != 0 {
		t.Fatalf("%+v", resp)
	}
	if textOutput(executeTest(t, s, `text(load("x"));`, Callbacks{})) != "1" {
		t.Fatal("exit did not commit")
	}
}

func TestNoAmbientHostCapabilitiesAndImages(t *testing.T) {
	s := testSession(t, Limits{})
	resp := executeTest(t, s, `text([typeof process,typeof require,typeof fetch,typeof console,typeof WebAssembly].join(","));`, Callbacks{})
	if textOutput(resp) != "undefined,undefined,undefined,undefined,undefined" {
		t.Fatalf("%+v", resp)
	}
	resp = executeTest(t, s, `image("data:image/png;base64,aGVsbG8=");generatedImage({image_url:"data:image/jpeg;base64,aGk="});`, Callbacks{})
	if resp.Status != "completed" || len(resp.Content) != 2 || resp.Content[0].Type != "image" || resp.Content[0].Data != "aGVsbG8=" {
		t.Fatalf("%+v", resp)
	}
	for _, source := range []string{`image("https://example.com/image.png")`, `image("/tmp/private.png")`, `import fs from "node:fs";`, `await import("node:fs");`} {
		if got := executeTest(t, s, source, Callbacks{}); got.Status != "failed" {
			t.Fatalf("%q: %+v", source, got)
		}
	}
}

func TestObservationBudgetsConsumeWithoutArtificialRunning(t *testing.T) {
	s := testSession(t, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := s.Execute(ctx, ExecuteRequest{ParentCallID: "trim", Source: `text("你好吗");`, MaxOutputBytes: 4, YieldTime: time.Second}, Callbacks{})
	if err != nil || resp.Status != "completed" || !resp.Truncated || len(resp.Content) != 2 || resp.Content[0].Text != "你" || !utf8.ValidString(textOutput(resp)) {
		t.Fatalf("%+v %v", resp, err)
	}
	resp, err = s.Execute(ctx, ExecuteRequest{ParentCallID: "zero", Source: `text("hidden");image("data:image/png;base64,aGk=");`, MaxOutputBytesSet: true, YieldTime: time.Second}, Callbacks{})
	if err != nil || resp.Status != "completed" || len(resp.Content) != 1 || resp.Content[0].Type != "image" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestLimitsAndCPUInterrupt(t *testing.T) {
	s := testSession(t, Limits{MaxExecutionTime: 20 * time.Millisecond, MaxOutputBytes: 100, MaxSourceBytes: 200, MaxStoreKeys: 1, MaxToolCalls: 2})
	start := time.Now()
	resp := executeTest(t, s, `store("discarded",1);while(true){}`, Callbacks{})
	if resp.Status != "failed" || !strings.Contains(resp.Error, "deadline") || time.Since(start) > time.Second {
		t.Fatalf("%+v duration=%v", resp, time.Since(start))
	}
	if out := textOutput(executeTest(t, s, `text(load("discarded"));`, Callbacks{})); out != "undefined" {
		t.Fatal(out)
	}
	for _, source := range []string{`text("x".repeat(101));`, `store("a",1);store("b",2);`, `store("a",()=>{});`, `const a={};a.self=a;store("a",a);`} {
		if got := executeTest(t, s, source, Callbacks{}); got.Status != "failed" {
			t.Fatalf("%q %+v", source, got)
		}
	}
	ctx := context.Background()
	if _, err := s.Execute(ctx, ExecuteRequest{ParentCallID: "spaces", Source: " \n\t "}, Callbacks{}); err == nil {
		t.Fatal("whitespace accepted")
	}
	if _, err := s.Execute(ctx, ExecuteRequest{ParentCallID: "huge", Source: strings.Repeat("x", 201)}, Callbacks{}); err == nil {
		t.Fatal("source limit ignored")
	}
}

func TestStoreCommitQuotaAtomicAcrossCells(t *testing.T) {
	s := testSession(t, Limits{MaxStoreKeys: 1})
	l := s.backend.(*localSession)
	if err := l.commit(map[string]json.RawMessage{"a": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	if err := l.commit(map[string]json.RawMessage{"a": json.RawMessage(`2`), "b": json.RawMessage(`3`)}); err == nil {
		t.Fatal("quota accepted")
	}
	if got := textOutput(executeTest(t, s, `text(load("a"));text(load("b"));`, Callbacks{})); got != "1undefined" {
		t.Fatal(got)
	}
}

func TestDispatcherFailureCannotBeCaughtByJS(t *testing.T) {
	s := testSession(t, Limits{})
	called := false
	resp := executeTest(t, s, `try { await tools.fail({}); } catch(e) { text("caught"); } await tools.effect({});`, Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
		if inv.Name == "fail" {
			return ToolResult{Terminate: true}, errors.New("fatal policy failure")
		}
		called = true
		return ToolResult{}, nil
	}}, ToolDefinition{Name: "fail"}, ToolDefinition{Name: "effect"})
	if resp.Status != "failed" || !strings.Contains(resp.Error, "fatal policy failure") || !resp.Terminate || called || len(resp.Content) != 0 {
		t.Fatalf("%+v laterEffect=%v", resp, called)
	}
	resp = executeTest(t, s, `const r=await tools.fail({});text(r.isError);`, Callbacks{Invoke: func(context.Context, Invocation) (ToolResult, error) { return ToolResult{IsError: true}, nil }}, ToolDefinition{Name: "fail"})
	if resp.Status != "completed" || textOutput(resp) != "true" {
		t.Fatalf("%+v", resp)
	}
}

func TestFatalCallbackInterruptsUnawaitedCPULoop(t *testing.T) {
	s := testSession(t, Limits{})
	start := time.Now()
	resp := executeTest(t, s, `tools.fail({});while(true){}`, Callbacks{Invoke: func(context.Context, Invocation) (ToolResult, error) {
		return ToolResult{}, errors.New("fatal background dispatcher error")
	}}, ToolDefinition{Name: "fail"})
	if resp.Status != "failed" || !strings.Contains(resp.Error, "fatal background dispatcher error") || time.Since(start) > time.Second {
		t.Fatalf("%+v elapsed=%v", resp, time.Since(start))
	}
}

func TestStackDepthLimit(t *testing.T) {
	s := testSession(t, Limits{MaxStackDepth: 32})
	resp := executeTest(t, s, `function recur(){return recur();}recur();`, Callbacks{})
	if resp.Status != "failed" || resp.Error == "" {
		t.Fatalf("%+v", resp)
	}
}

func TestNestedTerminateStopsLaterEffects(t *testing.T) {
	s := testSession(t, Limits{})
	called := false
	resp := executeTest(t, s, `await tools.stop({});await tools.effect({});text("wrong");`, Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
		if inv.Name == "stop" {
			return ToolResult{Terminate: true}, nil
		}
		called = true
		return ToolResult{}, nil
	}}, ToolDefinition{Name: "stop"}, ToolDefinition{Name: "effect"})
	if resp.Status != "completed" || !resp.Terminate || called || len(resp.Content) != 0 {
		t.Fatalf("%+v laterEffect=%v", resp, called)
	}
}

func TestUnawaitedNestedTerminateAndFatalRetainControl(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminate", true: "fatal"}[fatal], func(t *testing.T) {
			s := testSession(t, Limits{})
			resp := executeTest(t, s, `tools.stop({});while(true){}`, Callbacks{Invoke: func(context.Context, Invocation) (ToolResult, error) {
				if fatal {
					return ToolResult{Terminate: true}, errors.New("fatal terminate")
				}
				return ToolResult{Terminate: true}, nil
			}}, ToolDefinition{Name: "stop"})
			expected := "completed"
			if fatal {
				expected = "failed"
			}
			if resp.Status != expected || !resp.Terminate {
				t.Fatalf("%+v", resp)
			}
		})
	}
}

func TestAsyncExitAndPendingTimerDoNotKeepCellAlive(t *testing.T) {
	s := testSession(t, Limits{})
	resp := executeTest(t, s, `await new Promise(resolve=>setTimeout(resolve,0));store("exit",1);exit();text("wrong");`, Callbacks{})
	if resp.Status != "completed" || len(resp.Content) != 0 {
		t.Fatalf("%+v", resp)
	}
	resp = executeTest(t, s, `setTimeout(()=>text("wrong"),60000);text("done");`, Callbacks{})
	if resp.Status != "completed" || textOutput(resp) != "done" {
		t.Fatalf("%+v", resp)
	}
}

func TestNotificationFailureEndsPendingCell(t *testing.T) {
	s := testSession(t, Limits{})
	resp := executeTest(t, s, `notify("hello");await new Promise(()=>{});`, Callbacks{Notify: func(context.Context, Notification) error { return errors.New("notification audit failed") }})
	if resp.Status != "failed" || !strings.Contains(resp.Error, "notification audit failed") {
		t.Fatalf("%+v", resp)
	}
}
