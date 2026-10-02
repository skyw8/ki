package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/process"
	"ki/internal/types"
)

// The same test executable acts as a worker; no extra compiler/build per case.
func TestWorkerProcessHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != WorkerArg {
		return
	}
	if err := ServeWorker(context.Background(), os.Stdin, os.Stdout, DefaultLimits()); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func testManager(t *testing.T, limits Limits) *Manager {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{Executable: exe, Args: []string{"-test.run=^TestWorkerProcessHelper$", "--", WorkerArg}, Limits: limits})
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return m
}

func TestWorkerRoundtripAndSessionLifecycle(t *testing.T) {
	m := testManager(t, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := m.NewSession(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	remote := s.backend.(*remoteSession)
	if remote.transport != nil {
		t.Fatal("NewSession eagerly started worker")
	}
	notified := make(chan Notification, 1)
	cb := Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
		return ToolResult{Content: []types.Content{{Type: "text", Text: "full result"}}, Details: map[string]any{"count": 7}}, nil
	}, Notify: func(ctx context.Context, n Notification) error { notified <- n; return nil }}
	resp, err := s.Execute(ctx, ExecuteRequest{ParentCallID: "outer", Source: `const r=await tools.read({path:"x"});text(r.content[0].text);store("saved",r.details.count);notify("notice");`, Tools: []ToolDefinition{{Name: "read"}}, YieldTime: time.Second}, cb)
	if err != nil || resp.Status != "completed" || textOutput(resp) != "full resultnotice" {
		t.Fatalf("%+v %v", resp, err)
	}
	select {
	case n := <-notified:
		if n.CellID != resp.CellID || n.ParentCallID != "outer" {
			t.Fatalf("%+v", n)
		}
	default:
		t.Fatal("notify not delivered")
	}
	if err = s.TerminateAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, err = s.Execute(ctx, ExecuteRequest{ParentCallID: "later", Source: `text(load("saved"));text(typeof r);`, YieldTime: time.Second}, Callbacks{})
	if err != nil || resp.Status != "completed" || textOutput(resp) != "7undefined" {
		t.Fatalf("%+v %v", resp, err)
	}
	if err = m.CloseSession("session"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, ExecuteRequest{ParentCallID: "closed", Source: "text(1)"}, Callbacks{}); err == nil {
		t.Fatal("closed session accepted execution")
	}
	s, err = m.NewSession(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	resp, err = s.Execute(ctx, ExecuteRequest{ParentCallID: "fresh", Source: `text(load("saved"));`, YieldTime: time.Second}, Callbacks{})
	if err != nil || textOutput(resp) != "undefined" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestWorkerYieldAndTerminateJoinParentCallback(t *testing.T) {
	m := testManager(t, Limits{})
	s, err := m.NewSession(context.Background(), "join")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	cb := Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ToolResult{}, ctx.Err()
	}}
	resp, err := s.Execute(context.Background(), ExecuteRequest{ParentCallID: "join", Source: `store("aborted",1);yield_control();await tools.block({});`, Tools: []ToolDefinition{{Name: "block"}}, YieldTime: time.Second}, cb)
	if err != nil || !resp.Running() {
		t.Fatalf("%+v %v", resp, err)
	}
	<-started
	finished := make(chan error, 1)
	go func() { finished <- s.TerminateAll(context.Background()) }()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("parent callback not canceled")
	}
	select {
	case err := <-finished:
		t.Fatalf("cleanup returned before callback joined: %v", err)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	resp, err = s.Execute(context.Background(), ExecuteRequest{ParentCallID: "store", Source: `text(load("aborted"));`, YieldTime: time.Second}, Callbacks{})
	if err != nil || textOutput(resp) != "undefined" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestWorkerFatalErrorNotCatchable(t *testing.T) {
	m := testManager(t, Limits{})
	s, err := m.NewSession(context.Background(), "fatal")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	resp, err := s.Execute(context.Background(), ExecuteRequest{ParentCallID: "fatal", Source: `try {await tools.bad({});}catch(e){text("caught");}await tools.effect({});`, Tools: []ToolDefinition{{Name: "bad"}, {Name: "effect"}}, YieldTime: time.Second}, Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
		if inv.Name == "bad" {
			return ToolResult{}, errors.New("audit unavailable")
		}
		called = true
		return ToolResult{}, nil
	}})
	if err != nil || resp.Status != "failed" || !strings.Contains(resp.Error, "audit unavailable") || called {
		t.Fatalf("%+v %v called=%v", resp, err, called)
	}
}

func TestTerminationStopsWorkerBeforeCallbackError(t *testing.T) {
	limits := DefaultLimits()
	local := NewLocalSession(limits)
	parentRead, workerWrite := io.Pipe()
	workerRead, parentWrite := io.Pipe()
	remote := &remoteSession{ctx: context.Background(), config: Config{Limits: limits}, bindings: make(map[string]*callbackBinding)}
	returned := make(chan struct{})
	parent := newPeer(context.Background(), parentRead, parentWrite, limits.MaxFrameBytes, func(ctx context.Context, op string, data json.RawMessage) (any, error) {
		result, err := remote.callback(ctx, op, data)
		if op == "invoke" {
			close(returned)
		}
		return result, err
	})
	var worker *peer
	var stoppedCell *cell
	worker = newPeer(context.Background(), workerRead, workerWrite, limits.MaxFrameBytes, func(ctx context.Context, op string, data json.RawMessage) (any, error) {
		switch op {
		case "execute":
			var req ExecuteRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			return local.Execute(worker.ctx, req, Callbacks{Invoke: func(ctx context.Context, inv Invocation) (result ToolResult, err error) {
				err = worker.call(ctx, "invoke", inv, &result)
				return
			}})
		case "stopAll":
			local.backend.(*localSession).stopAll()
			return struct{}{}, nil
		case "terminateAll":
			// Force the cancellation error to finish the cell before join can
			// run. Without stopAll's ACK this commits writes deterministically,
			// instead of depending on subprocess/transport scheduling.
			select {
			case <-returned:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-stoppedCell.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return struct{}{}, local.TerminateAll(ctx)
		default:
			return nil, errors.New("unexpected worker operation")
		}
	})
	remote.transport = parent
	t.Cleanup(func() {
		parent.fail(context.Canceled)
		worker.fail(context.Canceled)
		_ = local.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	resp, err := remote.execute(ctx, ExecuteRequest{ParentCallID: "cancel-race", Source: `store("aborted",1);yield_control();await tools.block({});`, Tools: []ToolDefinition{{Name: "block"}}, YieldTime: time.Second}, Callbacks{Invoke: func(ctx context.Context, _ Invocation) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		return ToolResult{}, ctx.Err()
	}})
	if err != nil || !resp.Running() {
		t.Fatalf("%+v %v", resp, err)
	}
	l := local.backend.(*localSession)
	l.mu.Lock()
	stoppedCell = l.cells[resp.CellID]
	l.mu.Unlock()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("callback did not start")
	}
	if err := remote.terminateAll(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err = remote.execute(ctx, ExecuteRequest{ParentCallID: "store", Source: `text(load("aborted"));`, YieldTime: time.Second}, Callbacks{})
	if err != nil || textOutput(resp) != "undefined" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestWorkerCPUDeadlineAndCrashDoesNotReplay(t *testing.T) {
	m := testManager(t, Limits{MaxExecutionTime: 30 * time.Millisecond})
	s, err := m.NewSession(context.Background(), "cpu")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Execute(context.Background(), ExecuteRequest{ParentCallID: "loop", Source: `while(true){}`, YieldTime: time.Second}, Callbacks{})
	if err != nil || resp.Status != "failed" || !strings.Contains(resp.Error, "deadline") {
		t.Fatalf("%+v %v", resp, err)
	}
	remote := s.backend.(*remoteSession)
	process.KillProcessGroup(remote.cmd)
	select {
	case <-remote.processDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not exit")
	}
	if _, err = s.Execute(context.Background(), ExecuteRequest{ParentCallID: "afterCrash", Source: `text(1)`}, Callbacks{}); err == nil {
		t.Fatal("crashed worker silently restarted")
	}
	// Closing a failed transport reports the transport error but joins ownership.
	if err = m.CloseSession("cpu"); err == nil {
		t.Fatal("crash not reported on close")
	}
}

func TestWorkerMissingExecutableAndCapacity(t *testing.T) {
	m := NewManager(Config{Executable: "does-not-exist-code-mode-worker", MaxSessions: 1})
	t.Cleanup(func() { _ = m.Close() })
	s, err := m.NewSession(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.NewSession(context.Background(), "two"); err == nil {
		t.Fatal("capacity ignored")
	}
	if _, err = s.Execute(context.Background(), ExecuteRequest{ParentCallID: "missing", Source: "text(1)"}, Callbacks{}); err == nil {
		t.Fatal("missing executable accepted")
	}
	if err = m.CloseSession("one"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.NewSession(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
}

func TestTransportRejectsMalformedAndOversizeFrames(t *testing.T) {
	for _, data := range []string{strings.Repeat("x", 1000), `{"kind":"unknown","id":1}` + "\n", `{"kind":"request","id":0}` + "\n", "not-json\n"} {
		p := newPeer(context.Background(), strings.NewReader(data), io.Discard, 256, nil)
		select {
		case <-p.done:
		case <-time.After(time.Second):
			t.Fatal("malformed transport did not close")
		}
		if errors.Is(p.err(), io.EOF) {
			t.Fatalf("malformed frame accepted: %q", data)
		}
	}
}

func TestWorkerProtocolPipes(t *testing.T) {
	parentRead, workerWrite := io.Pipe()
	workerRead, parentWrite := io.Pipe()
	finished := make(chan error, 1)
	go func() { finished <- ServeWorker(context.Background(), workerRead, workerWrite, DefaultLimits()) }()
	p := newPeer(context.Background(), parentRead, parentWrite, DefaultLimits().MaxFrameBytes, func(ctx context.Context, op string, data json.RawMessage) (any, error) {
		return nil, errors.New("unexpected callback")
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.call(ctx, "init", DefaultLimits(), nil); err != nil {
		t.Fatal(err)
	}
	if err := p.call(ctx, "init", DefaultLimits(), nil); err == nil {
		t.Fatal("duplicate init accepted")
	}
	if err := p.call(ctx, "unknown", struct{}{}, nil); err == nil {
		t.Fatal("unknown operation accepted")
	}
	var response Response
	if err := p.call(ctx, "execute", ExecuteRequest{ParentCallID: "pipe", Source: "text(2)", YieldTime: time.Second}, &response); err != nil || textOutput(response) != "2" {
		t.Fatalf("%+v %v", response, err)
	}
	p.fail(context.Canceled)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRejectsForgedCapabilitiesAndGoObjects(t *testing.T) {
	ctx := context.Background()
	s := &remoteSession{config: Config{Limits: DefaultLimits()}, bindings: map[string]*callbackBinding{
		"outer": {ctx: ctx, cb: Callbacks{Invoke: func(context.Context, Invocation) (ToolResult, error) { return ToolResult{}, nil }}, tools: map[string]bool{"read": true}, freeform: map[string]bool{}, seen: map[string]bool{}, cellID: "cell-1"},
	}}
	for _, inv := range []Invocation{
		{ParentCallID: "unknown", CellID: "cell-1", Name: "read", ToolCallID: "cell-1/tool-1"},
		{ParentCallID: "outer", CellID: "cell-2", Name: "read", ToolCallID: "cell-2/tool-1"},
		{ParentCallID: "outer", CellID: "cell-1", Name: "disabled", ToolCallID: "cell-1/tool-1"},
		{ParentCallID: "outer", CellID: "cell-1", Name: "read", ToolCallID: "fake"},
		{ParentCallID: "outer", CellID: "cell-1", Name: "read", ToolCallID: "cell-1/tool-1", Freeform: true},
	} {
		data, _ := json.Marshal(inv)
		if _, err := s.callback(ctx, "invoke", data); err == nil {
			t.Fatalf("forged capability accepted: %+v", inv)
		}
	}
	inv := Invocation{ParentCallID: "outer", CellID: "cell-1", Name: "read", ToolCallID: "cell-1/tool-1"}
	data, _ := json.Marshal(inv)
	if _, err := s.callback(ctx, "invoke", data); err != nil {
		t.Fatal(err)
	}
	if _, err := s.callback(ctx, "invoke", data); err == nil {
		t.Fatal("duplicate invocation accepted")
	}
}

func TestWorkerEnvironmentDoesNotInheritProviderSecrets(t *testing.T) {
	t.Setenv("KI_TEST_PROVIDER_SECRET", "sensitive")
	for _, entry := range safeWorkerEnv() {
		if strings.HasPrefix(entry, "KI_TEST_PROVIDER_SECRET=") {
			t.Fatal("secret inherited")
		}
	}
}

func TestTransportOutboundFrameBound(t *testing.T) {
	r, w := io.Pipe()
	p := newPeer(context.Background(), r, &bytes.Buffer{}, 64, nil)
	defer p.fail(context.Canceled)
	defer w.Close()
	if err := p.send(context.Background(), wireFrame{Kind: "request", ID: 1, Data: json.RawMessage(`"` + strings.Repeat("a", 100) + `"`)}); err == nil {
		t.Fatal("outbound budget ignored")
	}
}

func TestCanceledTerminationStillJoinsParentOwnership(t *testing.T) {
	m := testManager(t, Limits{})
	s, err := m.NewSession(context.Background(), "cancelled-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = m.CloseSession("cancelled-cleanup") })
	resp, err := s.Execute(context.Background(), ExecuteRequest{ParentCallID: "cancelled", Source: `yield_control();await tools.block({});`, Tools: []ToolDefinition{{Name: "block"}}, YieldTime: time.Second}, Callbacks{Invoke: func(ctx context.Context, _ Invocation) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ToolResult{}, ctx.Err()
	}})
	if err != nil || !resp.Running() {
		t.Fatalf("%+v %v", resp, err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finished := make(chan error, 1)
	go func() { finished <- s.TerminateAll(ctx) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("parent ownership not cancelled")
	}
	select {
	case err := <-finished:
		t.Fatalf("cancelled cleanup returned before join: %v", err)
	default:
	}
	once.Do(func() { close(release) })
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cleanup error %v", err)
	}
}

func TestTerminalExecuteJoinsItsUnawaitedParentCallback(t *testing.T) {
	m := testManager(t, Limits{})
	s, err := m.NewSession(context.Background(), "completion-fence")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	type result struct {
		response Response
		err      error
	}
	finished := make(chan result, 1)
	go func() {
		resp, err := s.Execute(context.Background(), ExecuteRequest{ParentCallID: "completion", Source: `tools.block({});await tools.started({});text("done");`, Tools: []ToolDefinition{{Name: "block"}, {Name: "started"}}, YieldTime: time.Second}, Callbacks{Invoke: func(ctx context.Context, inv Invocation) (ToolResult, error) {
			if inv.Name == "started" {
				select {
				case <-started:
					return ToolResult{}, nil
				case <-ctx.Done():
					return ToolResult{}, ctx.Err()
				}
			}
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return ToolResult{}, ctx.Err()
		}})
		finished <- result{resp, err}
	}()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("completion did not cancel unawaited observation")
	}
	select {
	case r := <-finished:
		t.Fatalf("terminal Execute returned before host callback joined: %+v", r)
	default:
	}
	once.Do(func() { close(release) })
	r := <-finished
	if r.err != nil || r.response.Status != "completed" || textOutput(r.response) != "done" {
		t.Fatalf("%+v %v", r.response, r.err)
	}
}

func TestCanceledWaitFencesCallbacksWithoutKillingWorker(t *testing.T) {
	m := testManager(t, Limits{})
	s, err := m.NewSession(context.Background(), "cancelled-wait")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	resp, err := s.Execute(context.Background(), ExecuteRequest{ParentCallID: "wait-cancel", Source: `yield_control();await tools.block({});`, Tools: []ToolDefinition{{Name: "block"}}, YieldTime: time.Second}, Callbacks{Invoke: func(ctx context.Context, _ Invocation) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ToolResult{}, ctx.Err()
	}})
	if err != nil || !resp.Running() {
		t.Fatalf("%+v %v", resp, err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finished := make(chan error, 1)
	go func() { _, err := s.Wait(ctx, WaitRequest{CellID: resp.CellID}); finished <- err }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("canceled Wait did not cancel callback")
	}
	select {
	case err := <-finished:
		t.Fatalf("canceled Wait returned before callback joined: %v", err)
	default:
	}
	once.Do(func() { close(release) })
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fresh, err := s.Execute(context.Background(), ExecuteRequest{ParentCallID: "after-cancelled-wait", Source: `text("alive");`, YieldTime: time.Second}, Callbacks{})
	if err != nil || fresh.Status != "completed" || textOutput(fresh) != "alive" {
		t.Fatalf("healthy worker lost after observation cancellation: %+v %v", fresh, err)
	}
}
