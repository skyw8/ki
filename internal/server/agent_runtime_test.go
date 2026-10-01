package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ki/internal/agent"
	"ki/internal/process"
	"ki/internal/tool/builtin"
)

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
	// Tree cleanup releases its admission fence; explicit work can reuse the identity.
	result, err := srv.SendAgentMessage(t.Context(), agent.MessageRequest{SenderSessionID: root, Target: launch.TaskPath, TriggerTurn: true, Message: "continue"})
	if err != nil || result.Status != "resumed" {
		t.Fatalf("reuse after tree abort: %+v %v", result, err)
	}
	if _, err := srv.InterruptAgent(context.Background(), root, launch.TaskPath); err != nil {
		t.Fatal(err)
	}
}
