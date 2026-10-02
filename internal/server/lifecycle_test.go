package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"ki/internal/agent"
	"ki/internal/config"
	"ki/internal/extension"
	"ki/internal/loop"
	"ki/internal/process"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/tool/builtin"
	"ki/internal/types"
)

func lifecycleServer(t *testing.T, streamer loop.Streamer) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	cfg := config.Builtin(home)
	cfg.Sessions.Root = filepath.Join(home, "sessions")
	srv, err := New(Options{Config: cfg, Token: "tok", Streamer: streamer})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

func lifecycleSession(t *testing.T, srv *Server) (id, dir string) {
	t.Helper()
	ref := srv.registry.Default()
	sess, err := session.Create(srv.cfg.Sessions.Root, t.TempDir(), ref.Provider, ref.Model)
	if err != nil {
		t.Fatal(err)
	}
	id, dir = sess.ID(), sess.Dir
	srv.sidx.Add(id, dir)
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	return id, dir
}

func assertLateExecClosed(t *testing.T, srv *Server, id string) {
	t.Helper()
	manager := srv.processesFor(id)
	if manager == nil {
		t.Fatal("nil would let builtin.Set create an unowned standalone manager")
	}
	for _, tool := range (builtin.Set{CWD: t.TempDir(), Processes: manager, Shells: srv.shells}).Build(builtin.Profile{}) {
		if tool.Name() == "exec_command" {
			result := tool.Execute(context.Background(), map[string]any{"cmd": "exit 0", "yield_time_ms": 250})
			if !result.IsError {
				t.Fatalf("late extension/catalog execution escaped lifecycle fence: %+v", result)
			}
			return
		}
	}
	t.Fatal("exec_command unavailable")
}

func TestShutdownDrainsScheduledRootBeforeProcessCleanup(t *testing.T) {
	srv := lifecycleServer(t, &provider.Scripted{})
	id, _ := lifecycleSession(t, srv)
	st, runCtx, err := srv.occupy(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	manager := srv.processesFor(id)
	if manager == nil {
		t.Fatal("missing manager before shutdown")
	}
	spool, err := srv.outputStore.CreateOutputFile(id, "exec_command")
	if err != nil {
		t.Fatal(err)
	}
	path := spool.Name()
	spool.Close()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := srv.Shutdown(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("deadline: %v", err)
	}
	<-runCtx.Done()
	assertLateExecClosed(t, srv, id)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cleanup closed root resources before the scheduled writer: %v", err)
	}
	select {
	case <-srv.shutdownDone:
		t.Fatal("shutdown completed before runPrompt started")
	default:
	}
	// occupy -> scheduled runPrompt is a real asynchronous boundary. A cancelled
	// scheduled callback must release ownership without recreating resources.
	srv.runPrompt(runCtx, st, id, []types.Content{{Type: "text", Text: "task"}}, nil, "", "", "", nil)
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	count := len(srv.processes)
	srv.mu.Unlock()
	if count != 0 {
		t.Fatalf("late process managers: %d", count)
	}
	if _, err := os.Stat(srv.outputStore.Root()); !os.IsNotExist(err) {
		t.Fatalf("output root survived shutdown: %v", err)
	}
}

type lifecycleCleanupStreamer struct {
	started   chan string
	cancelled chan string
	rootGate  chan struct{}
	childGate chan struct{}
	root      string
}

func (g *lifecycleCleanupStreamer) Stream(ctx context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	g.started <- req.SessionID
	<-ctx.Done()
	g.cancelled <- req.SessionID
	gate := g.childGate
	if req.SessionID == g.root {
		gate = g.rootGate
	}
	<-gate
	return types.Message{}, ctx.Err()
}

func TestShutdownDeadlineSharesRootAndChildCleanupBarrier(t *testing.T) {
	g := &lifecycleCleanupStreamer{
		started: make(chan string, 2), cancelled: make(chan string, 2),
		rootGate: make(chan struct{}), childGate: make(chan struct{}),
	}
	releaseRoot := sync.OnceFunc(func() { close(g.rootGate) })
	releaseChild := sync.OnceFunc(func() { close(g.childGate) })
	srv := lifecycleServer(t, g)
	// Unblock before lifecycleServer's cleanup even when an assertion fails.
	t.Cleanup(releaseChild)
	t.Cleanup(releaseRoot)
	root, _ := lifecycleSession(t, srv)
	g.root = root
	st, runCtx, err := srv.occupy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	go srv.runPrompt(runCtx, st, root, []types.Content{{Type: "text", Text: "root task"}}, nil, "", "", "", nil)
	if _, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: "child", ParentSessionID: root, Prompt: "child task", ForkTurns: "none"}); err != nil {
		t.Fatal(err)
	}
	<-g.started
	<-g.started
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := srv.Shutdown(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("deadline: %v", err)
	}
	<-g.cancelled
	<-g.cancelled
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- srv.Shutdown(context.Background()) }()
	}
	releaseRoot()
	<-st.released
	select {
	case <-srv.shutdownDone:
		t.Fatal("root release abandoned the child's callback")
	default:
	}
	if _, err := os.Stat(srv.outputStore.Root()); err != nil {
		t.Fatalf("deadline closed shared output under child cleanup: %v", err)
	}
	releaseChild()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteFencesQueuedReleaseAndReservedSpawn(t *testing.T) {
	srv := lifecycleServer(t, &provider.Scripted{})
	root, dir := lifecycleSession(t, srv)
	other, otherDir := lifecycleSession(t, srv)
	parent, err := srv.open(root)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := session.Fork(srv.cfg.Sessions.Root, parent)
	parent.Close()
	if err != nil {
		t.Fatal(err)
	}
	flatID, flatDir := flat.ID(), flat.Dir
	flat.Close()
	srv.sidx.Add(flatID, flatDir)
	reservation, err := srv.agentTasks.ReservePath(root, "/root/late")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reservation)
	st, runCtx, err := srv.occupy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Enqueue(dir, []types.Content{{Type: "text", Text: "queued user task"}}); err != nil {
		t.Fatal(err)
	}
	// Park release after done but before queue dispatch to make the formerly
	// unsafe done -> directory removal interval deterministic.
	gate := srv.inputGate(root)
	gate.Lock()
	unlock := sync.OnceFunc(gate.Unlock)
	t.Cleanup(unlock)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/v1/sessions/"+root, nil)
	request.SetPathValue("id", root)
	deleted := make(chan struct{})
	go func() {
		srv.deleteSession(recorder, request)
		close(deleted)
	}()
	<-runCtx.Done()
	if _, _, err := srv.occupy(t.Context(), root); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("deleting session reoccupied: %v", err)
	}
	assertLateExecClosed(t, srv, root)
	if _, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: "new", ParentSessionID: root, Prompt: "task"}); err == nil {
		t.Fatal("deletion admitted a child after traversal")
	}
	go srv.release(root, st)
	<-st.done
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory removed under release callback: %v", err)
	}
	unlock()
	<-st.released
	select {
	case <-deleted:
		t.Fatal("deletion did not drain the pre-fence spawn reservation")
	default:
	}
	reservation()
	<-deleted
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("deleted session files survived: %v", err)
	}
	if _, ok := srv.sidx.Lookup(root); ok {
		t.Fatal("deleted session remained indexed")
	}
	if srv.running(root) {
		t.Fatal("queued release reoccupied deleted session")
	}
	if _, err := srv.Enqueue(root, "test", extension.EnqueueRequest{Content: []types.Content{{Type: "text", Text: "late task"}}}); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("extension queue after deletion: %v", err)
	}
	if _, err := srv.AppendMessage(root, "test", extension.AppendMessageRequest{Message: types.Message{Content: []types.Content{{Type: "text", Text: "late context"}}}}); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("extension context after deletion: %v", err)
	}
	if err := srv.AppendEntry(root, "test", "late", nil); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("extension entry after deletion: %v", err)
	}
	if _, err := srv.acceptAgentContext(root, "agent:other", "late message", nil); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("agent context after deletion: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("late input recreated deleted files: %v", err)
	}
	for id, dir := range map[string]string{other: otherDir, flatID: flatDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("unrelated/flat session %s removed: %v", id, err)
		}
	}
}

func TestDeleteDrainsAgentCompletionCallback(t *testing.T) {
	synctest.Test(t, testDeleteDrainsAgentCompletionCallback)
}

func testDeleteDrainsAgentCompletionCallback(t *testing.T) {
	srv := lifecycleServer(t, agentEchoStreamer{})
	root, _ := lifecycleSession(t, srv)
	parent, err := srv.open(root)
	if err != nil {
		t.Fatal(err)
	}
	child, err := session.CreateChild(srv.cfg.Sessions.Root, parent)
	parent.Close()
	if err != nil {
		t.Fatal(err)
	}
	childID, childDir := child.ID(), child.Dir
	child.Close()
	srv.sidx.Add(childID, childDir)
	callbackStarted := make(chan struct{})
	callbackGate := make(chan struct{})
	callbackFinished := make(chan bool, 1)
	releaseCallback := sync.OnceFunc(func() { close(callbackGate) })
	t.Cleanup(releaseCallback)
	req := agent.Request{
		TaskName: "child", TaskPath: "/root/child", RootSessionID: root,
		ParentSessionID: root, SessionID: childID, Prompt: "child task",
		MetadataPath: filepath.Join(childDir, "agent.json"),
		OutputFile:   filepath.Join(childDir, "events.jsonl"),
	}
	_, err = srv.agentTasks.Start(t.Context(), req, req.OutputFile, func(ctx context.Context, taskID, prompt string) (agent.Completion, error) {
		req.Prompt = prompt
		completion, runErr := srv.runChildAgent(ctx, childID, req)
		// This is the real server boundary: runPrompt has released the child
		// occupy, but its agentRunner completion handler is still a writer.
		close(callbackStarted)
		<-callbackGate
		_, exists := srv.sidx.Lookup(childID)
		_, fileErr := os.Stat(req.OutputFile)
		snapshot, _ := srv.agentTasks.Get(taskID)
		srv.notifyAgentCompletion(root, types.CompletionIdentity{TaskID: taskID, Generation: snapshot.Generation}, "child", req.OutputFile, completion, runErr)
		callbackFinished <- exists && fileErr == nil
		return completion, runErr
	})
	if err != nil {
		t.Fatal(err)
	}
	<-callbackStarted
	st, runCtx, err := srv.occupy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/v1/sessions/"+root, nil)
	request.SetPathValue("id", root)
	deleted := make(chan struct{})
	go func() {
		srv.deleteSession(recorder, request)
		close(deleted)
	}()
	<-runCtx.Done()
	srv.release(root, st)
	// Stop now waits on the real generation's runDone channel, not a mutex.
	// synctest.Wait proves deletion reached that wait before the assertion.
	synctest.Wait()
	if _, ok := srv.sidx.Lookup(childID); !ok {
		t.Fatal("occupy settlement was mistaken for callback completion")
	}
	select {
	case <-deleted:
		t.Fatal("delete returned with the agent completion handler still writing")
	default:
	}
	releaseCallback()
	if exists := <-callbackFinished; !exists {
		t.Fatal("completion handler outlived its session resources")
	}
	<-deleted
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := srv.sidx.Lookup(childID); ok {
		t.Fatal("deleted child identity remained indexed")
	}
	if _, ok := srv.sidx.Lookup(root); ok {
		t.Fatal("deleted root identity remained indexed")
	}
	if _, err := os.Stat(childDir); !os.IsNotExist(err) {
		t.Fatalf("child files survived callback-drained deletion: %v", err)
	}
}

func TestShutdownStopsTerminalWhileRootCallbackIsStalled(t *testing.T) {
	srv := lifecycleServer(t, &provider.Scripted{})
	root, _ := lifecycleSession(t, srv)
	shell, err := process.ResolveShell(srv.shells, "", false)
	if err != nil {
		t.Skip("default shell unavailable")
	}
	command := "sleep 120"
	if srv.shells.PowerShellEnabled() {
		command = "Start-Sleep -Seconds 120"
	}
	manager := srv.processesFor(root)
	stopped := make(chan struct{})
	stopOnce := sync.OnceFunc(func() { close(stopped) })
	manager.SetListener(func(update process.Update) {
		if update.Process.Status == "exited" {
			stopOnce()
		}
	})
	if _, err := manager.Start(t.Context(), shell, t.TempDir(), command, false, process.Identity{}); err != nil {
		t.Fatal(err)
	}
	st, runCtx, err := srv.occupy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := srv.Shutdown(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("deadline: %v", err)
	}
	<-runCtx.Done()
	<-stopped // host shutdown must terminate OS handles before the root unblocks
	select {
	case <-srv.shutdownDone:
		t.Fatal("stalled root ownership was abandoned")
	default:
	}
	if _, err := os.Stat(srv.outputStore.Root()); err != nil {
		t.Fatalf("terminal stop removed shared output before root cleanup: %v", err)
	}
	srv.release(root, st)
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type lifecycleBodyGate struct {
	reader  io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *lifecycleBodyGate) Read(p []byte) (int, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	return g.reader.Read(p)
}

func TestDeleteIncludesPreacceptedHTTPFork(t *testing.T) {
	srv := lifecycleServer(t, &provider.Scripted{})
	root, _ := lifecycleSession(t, srv)
	parent, err := srv.open(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := parent.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "settled prefix"}}})
	parent.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, runCtx, err := srv.occupy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	body := &lifecycleBodyGate{
		reader:  strings.NewReader(`{"entryId":"` + entry.ID + `","forkMode":"tree"}`),
		started: make(chan struct{}), release: make(chan struct{}),
	}
	releaseBody := sync.OnceFunc(func() { close(body.release) })
	t.Cleanup(releaseBody)
	forkResponse := httptest.NewRecorder()
	forkRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+root+"/fork", body)
	forkRequest.SetPathValue("id", root)
	forked := make(chan struct{})
	go func() {
		srv.fork(forkResponse, forkRequest)
		close(forked)
	}()
	<-body.started // fork already owns the input gate and passed admission
	deleteResponse := httptest.NewRecorder()
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/v1/sessions/"+root, nil)
	deleteRequest.SetPathValue("id", root)
	deleted := make(chan struct{})
	go func() {
		srv.deleteSession(deleteResponse, deleteRequest)
		close(deleted)
	}()
	<-runCtx.Done() // traversal fenced the root while its fork is materializing
	releaseBody()
	<-forked
	if forkResponse.Code != http.StatusOK {
		t.Fatalf("preaccepted fork: %d %s", forkResponse.Code, forkResponse.Body.String())
	}
	var child struct {
		ID  string `json:"id"`
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal(forkResponse.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	srv.release(root, st)
	<-deleted
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if _, ok := srv.sidx.Lookup(child.ID); ok {
		t.Fatal("preaccepted tree fork survived deletion traversal")
	}
	if _, err := os.Stat(child.Dir); !os.IsNotExist(err) {
		t.Fatalf("preaccepted tree fork files survived: %v", err)
	}
}

type lifecycleQueuedStreamer struct {
	started chan struct{}
}

func (g lifecycleQueuedStreamer) Stream(ctx context.Context, _ loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	close(g.started)
	<-ctx.Done()
	return types.Message{}, ctx.Err()
}

func TestTreeAbortDefersQueuedRootUntilCleanup(t *testing.T) {
	g := lifecycleQueuedStreamer{started: make(chan struct{})}
	srv := lifecycleServer(t, g)
	root, dir := lifecycleSession(t, srv)
	st, runCtx, err := srv.occupy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := srv.agentTasks.ReservePath(root, "/root/late")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reservation)
	if _, err := session.Enqueue(dir, []types.Content{{Type: "text", Text: "queued root task"}}); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		srv.stopRuntimeTree(root)
		close(stopped)
	}()
	<-runCtx.Done()
	srv.release(root, st)
	queued, err := session.ReadQueue(dir)
	if err != nil || len(queued) != 1 {
		t.Fatalf("tree cleanup lost/started queued input: %v %v", queued, err)
	}
	select {
	case <-g.started:
		t.Fatal("replacement root started before reserved spawn cleanup")
	default:
	}
	reservation()
	<-stopped
	<-g.started
}
