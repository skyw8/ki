package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ki/internal/agent"
	"ki/internal/loop"
	"ki/internal/process"
	"ki/internal/tool/builtin"
	"ki/internal/types"
)

type cascadeStreamer struct {
	started chan struct{}
}

func (s *cascadeStreamer) Stream(ctx context.Context, _ loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	return types.Message{}, ctx.Err()
}

func TestAgentInterruptCascadesThroughIdleParentsAndRejectsAncestors(t *testing.T) {
	stream := &cascadeStreamer{started: make(chan struct{}, 8)}
	srv, hs := testServerWith(t, stream)
	srv.agentTasks.SetMaxConcurrent(8)
	root := createSession(t, hs, t.TempDir())
	spawn := func(parent, name string) agent.Launch {
		t.Helper()
		launch, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: name, ParentSessionID: parent, Prompt: "wait", ForkTurns: "none"})
		if err != nil {
			t.Fatal(err)
		}
		<-stream.started
		return launch
	}
	child := spawn(root, "child")
	grandchild := spawn(child.SessionID, "grandchild")
	deep := spawn(grandchild.SessionID, "deep")
	sibling := spawn(root, "sibling")
	if _, err := srv.InterruptAgent(t.Context(), deep.SessionID, child.TaskPath); err == nil {
		t.Fatal("ancestor interruption would wait on its caller")
	}
	for _, launch := range []agent.Launch{child, grandchild, deep} {
		if _, err := srv.SendAgentMessage(t.Context(), agent.MessageRequest{SenderSessionID: root, Target: launch.TaskPath, Message: "discard", TriggerTurn: true}); err != nil {
			t.Fatal(err)
		}
	}
	previous, err := srv.InterruptAgent(t.Context(), root, child.TaskPath)
	if err != nil || previous.Status != agent.Running {
		t.Fatalf("interrupt receipt: %+v %v", previous, err)
	}
	for _, launch := range []agent.Launch{child, grandchild, deep} {
		snapshot, ok := srv.Get(launch.TaskID)
		if !ok || snapshot.Status != agent.Killed || snapshot.PendingTasks != 0 || snapshot.Generation != 1 {
			t.Fatalf("descendant survived: %+v", snapshot)
		}
	}
	if snapshot, _ := srv.Get(sibling.TaskID); snapshot.Status != agent.Running {
		t.Fatalf("sibling stopped: %+v", snapshot)
	}
	if _, err := srv.SendAgentMessage(t.Context(), agent.MessageRequest{SenderSessionID: root, Target: deep.TaskPath, Message: "resume", TriggerTurn: true}); err != nil {
		t.Fatal(err)
	}
	<-stream.started
	if _, err := srv.InterruptAgent(t.Context(), root, child.TaskPath); err != nil {
		t.Fatalf("idle parent did not cascade: %v", err)
	}
	if snapshot, _ := srv.Get(deep.TaskID); snapshot.Status != agent.Killed || snapshot.Generation != 2 {
		t.Fatalf("resumed descendant survived idle parent: %+v", snapshot)
	}
}

func TestSessionTurnAbortCascadesFromRootAndParent(t *testing.T) {
	for _, target := range []string{"root", "child"} {
		t.Run(target, func(t *testing.T) {
			stream := &cascadeStreamer{started: make(chan struct{}, 8)}
			srv, hs := testServerWith(t, stream)
			root := createSession(t, hs, t.TempDir())
			child, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: "child", ParentSessionID: root, Prompt: "wait", ForkTurns: "none"})
			if err != nil {
				t.Fatal(err)
			}
			<-stream.started
			grandchild, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: "grandchild", ParentSessionID: child.SessionID, Prompt: "wait", ForkTurns: "none"})
			if err != nil {
				t.Fatal(err)
			}
			<-stream.started
			if _, err := srv.SendAgentMessage(t.Context(), agent.MessageRequest{SenderSessionID: root, Target: grandchild.TaskPath, Message: "discard", TriggerTurn: true}); err != nil {
				t.Fatal(err)
			}
			id := root
			if target == "child" {
				id = child.SessionID
			}
			status, result := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/abort", map[string]any{"scope": "turn"})
			if status != http.StatusOK || result["aborted"] != true {
				t.Fatalf("abort response: %d %+v", status, result)
			}
			for _, launch := range []agent.Launch{child, grandchild} {
				snapshot, _ := srv.Get(launch.TaskID)
				if snapshot.Status != agent.Killed || snapshot.PendingTasks != 0 {
					t.Fatalf("turn abort missed descendant: %+v", snapshot)
				}
			}
		})
	}
}

func TestExtensionAbortCancelsOwnTurnBeforeWaitingForDescendants(t *testing.T) {
	stream := &cascadeStreamer{started: make(chan struct{}, 8)}
	srv, hs := testServerWith(t, stream)
	root := createSession(t, hs, t.TempDir())
	st, runCtx, err := srv.occupy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.release(root, st)
	child, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: "child", ParentSessionID: root, Prompt: "wait", ForkTurns: "none"})
	if err != nil {
		t.Fatal(err)
	}
	<-stream.started
	if err := srv.Abort(root); err != nil {
		t.Fatal(err)
	}
	if runCtx.Err() != context.Canceled {
		t.Fatal("extension abort omitted own cancellation")
	}
	if snapshot, _ := srv.Get(child.TaskID); snapshot.Status != agent.Killed {
		t.Fatalf("extension abort omitted descendant: %+v", snapshot)
	}
}

func TestTreeAbortStopsOwnedProcessesAndPreservesUnrelatedRoot(t *testing.T) {
	shells := process.DiscoverShellRuntime()
	if !shells.BashAvailable() {
		t.Skip("Bash unavailable for POSIX process fixture")
	}
	stream := &completionRaceStreamer{started: make(chan struct{}), release: make(chan struct{})}
	srv, hs := testServerWith(t, stream)
	cwd := t.TempDir()
	root := createSession(t, hs, cwd)
	other := createSession(t, hs, t.TempDir())
	launch, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: "child", ParentSessionID: root, Prompt: "task", ForkTurns: "none"})
	if err != nil {
		t.Fatal(err)
	}
	<-stream.started
	startProcess := func(owner string) process.Snapshot {
		t.Helper()
		set := builtin.Set{CWD: cwd, Shells: shells, Processes: srv.processesFor(owner)}
		for _, tool := range set.Build(builtin.Profile{}) {
			if tool.Name() != "exec_command" {
				continue
			}
			result := tool.Execute(t.Context(), map[string]any{"cmd": "sleep 120", "shell": "bash", "yield_time_ms": 250})
			var value process.Snapshot
			if result.IsError || len(result.Content) == 0 {
				t.Fatalf("start process: %+v", result)
			}
			if err := json.Unmarshal([]byte(result.Content[0].Text), &value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		t.Fatal("exec_command unavailable")
		return process.Snapshot{}
	}
	rootProcess := startProcess(root)
	childProcess := startProcess(launch.SessionID)
	otherProcess := startProcess(other)
	if _, err := srv.InterruptAgent(t.Context(), root, launch.TaskPath); err != nil {
		t.Fatal(err)
	}
	if child := srv.processSnapshots(launch.SessionID); len(child) != 1 || child[0].Status != "running" {
		t.Fatalf("turn interruption killed owned process: %+v", child)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+root+"/abort", strings.NewReader(`{"source":"test","scope":"tree"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer tok")
	request.Header.Set("Content-Type", "application/json")
	response, err := hs.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tree abort: %s", response.Status)
	}
	response.Body.Close()
	for _, owner := range []string{root, launch.SessionID} {
		for _, process := range srv.processSnapshots(owner) {
			if process.Status != "exited" {
				t.Fatalf("process %d survived tree abort: %+v", process.SessionID, process)
			}
		}
	}
	if rootProcess.SessionID == childProcess.SessionID || childProcess.SessionID == otherProcess.SessionID {
		t.Fatal("process identity collision")
	}
	if processes := srv.processSnapshots(other); len(processes) != 1 || processes[0].Status != "running" {
		t.Fatalf("another root was stopped: %+v", processes)
	}
	result, err := srv.SendAgentMessage(t.Context(), agent.MessageRequest{SenderSessionID: root, Target: launch.TaskPath, TriggerTurn: true, Message: "continue"})
	if err != nil || result.Status != "resumed" {
		t.Fatalf("reuse after tree abort: %+v %v", result, err)
	}
	if _, err := srv.InterruptAgent(context.Background(), root, launch.TaskPath); err != nil {
		t.Fatal(err)
	}
}
