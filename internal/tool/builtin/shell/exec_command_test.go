package shelltools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ki/internal/process"
	toolapi "ki/internal/tool"
	filetools "ki/internal/tool/builtin/file"
)

func testExec(t *testing.T, spool process.OutputSpool) execCommandTool {
	t.Helper()
	manager := process.NewSpooledManager(spool, "test-session")
	t.Cleanup(manager.Close)
	shells := process.DiscoverShellRuntime()
	if !shells.BashAvailable() {
		t.Skip("Bash unavailable for POSIX command fixtures")
	}
	shells = shells.WithoutPowerShell()
	return execCommandTool{cwd: t.TempDir(), processes: manager, shells: shells}
}

func execSnapshot(t *testing.T, result toolapi.Result) process.Snapshot {
	t.Helper()
	var snap process.Snapshot
	if len(result.Content) == 0 {
		t.Fatal("no process result")
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &snap); err != nil {
		t.Fatalf("decode: %v result=%+v", err, result)
	}
	return snap
}

func TestExecNonZeroAndCwdReset(t *testing.T) {
	tool := testExec(t, nil)
	result := tool.Execute(t.Context(), map[string]any{"cmd": "exit 3"})
	snap := execSnapshot(t, result)
	if !result.IsError || snap.ExitCode == nil || *snap.ExitCode != 3 {
		t.Fatalf("nonzero: %+v", result)
	}
	child := filepath.Join(tool.cwd, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	snap = execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "pwd", "workdir": child}))
	if !strings.Contains(snap.Output, "child") {
		t.Fatalf("cwd: %+v", snap)
	}
	snap = execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "pwd"}))
	if strings.Contains(snap.Output, "child") {
		t.Fatalf("cwd persisted: %+v", snap)
	}
}

func TestExecObserverCancelPreservesProcessAndExplicitStopKillsTree(t *testing.T) {
	tool := testExec(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan toolapi.Result, 1)
	go func() { done <- tool.Execute(ctx, map[string]any{"cmd": "sleep 120 & echo $! > child.pid; wait"}) }()
	pidPath := filepath.Join(tool.cwd, "child.pid")
	var pid int
	waitFor(t, func() bool {
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		return pid > 0
	}, "child did not start")
	cancel()
	result := <-done
	snap := execSnapshot(t, result)
	if !result.IsError || snap.Status != "running" {
		t.Fatalf("observer cancellation killed process: %+v", result)
	}
	probe := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": fmt.Sprintf("kill -0 %d 2>/dev/null && echo alive", pid)}))
	if !strings.Contains(probe.Output, "alive") {
		t.Fatal("descendant no longer alive")
	}
	stopped, err := tool.processes.Terminate(snap.SessionID)
	if err != nil || stopped.Status != "exited" {
		t.Fatalf("terminate: %+v %v", stopped, err)
	}
	waitFor(t, func() bool {
		probe := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": fmt.Sprintf("kill -0 %d 2>/dev/null || echo gone", pid)}))
		return strings.Contains(probe.Output, "gone")
	}, "descendant survived tree termination")
}

func TestExecYieldAndIncrementalOutput(t *testing.T) {
	tool := testExec(t, nil)
	first := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "printf 'one\n'; sleep 0.6; printf 'two\n'", "yield_time_ms": 250}))
	if first.Status != "running" || first.Output != "one\n" {
		t.Fatalf("initial yield: %+v", first)
	}
	next := execSnapshot(t, (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": first.SessionID, "yield_time_ms": 5000}))
	if next.Status != "exited" || next.Output != "two\n" || next.OutputOffset != 4 {
		t.Fatalf("incremental result: %+v", next)
	}
	again := execSnapshot(t, (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": first.SessionID}))
	if again.Output != "" {
		t.Fatalf("output consumed twice: %+v", again)
	}
	raw, err := os.ReadFile(first.OutputFile)
	if err != nil || string(raw) != "one\ntwo\n" {
		t.Fatalf("spool: %q %v", raw, err)
	}
}

func TestExecLargeOutputSpoolsAndReadPages(t *testing.T) {
	tool := testExec(t, nil)
	snap := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "i=1; while [ $i -le 8000 ]; do printf 'line-%05d\n' \"$i\"; i=$((i+1)); done", "max_output_tokens": 32}))
	if !snap.Truncated || len(snap.Output) > 128 || !strings.Contains(snap.Output, "line-00001") {
		t.Fatalf("bounded output: %+v", snap)
	}
	raw, err := os.ReadFile(snap.OutputFile)
	if err != nil || !strings.Contains(string(raw), "line-00001\n") || !strings.Contains(string(raw), "line-08000\n") {
		t.Fatalf("incomplete spool: %v", err)
	}
	result := (filetools.Set{CWD: tool.cwd}).Build(false, false)[0].Execute(t.Context(), map[string]any{"file_path": snap.OutputFile, "offset": 1, "limit": 2})
	if result.IsError || !strings.Contains(result.Content[0].Text, "line-00001\nline-00002") {
		t.Fatalf("Read spool: %+v", result)
	}
	next := execSnapshot(t, (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": snap.SessionID, "max_output_tokens": 32}))
	if next.OutputOffset != int64(len(snap.Output)) || next.Output == snap.Output {
		t.Fatalf("cursor: %+v", next)
	}
}

func TestExecSmallOutputAndFinishedStop(t *testing.T) {
	tool := testExec(t, nil)
	result := tool.Execute(t.Context(), map[string]any{"cmd": "printf small"})
	snap := execSnapshot(t, result)
	if result.IsError || snap.Output != "small" || snap.Truncated {
		t.Fatalf("small: %+v", result)
	}
	stopped, err := tool.processes.Terminate(snap.SessionID)
	if err != nil || stopped.Status != "exited" {
		t.Fatalf("finished stop: %+v %v", stopped, err)
	}
}

func TestExecPreservesToolExecutionAttribution(t *testing.T) {
	command := testExec(t, nil)
	identity := toolapi.ExecutionIdentity{RunID: "run", CallID: "call", AgentID: "agent", Generation: 3}
	ctx := toolapi.WithExecutionIdentity(t.Context(), identity)
	result := command.Execute(ctx, map[string]any{"cmd": "printf attributed"})
	snapshot := execSnapshot(t, result)
	if result.IsError || snapshot.RunID != identity.RunID || snapshot.CallID != identity.CallID || snapshot.AgentID != identity.AgentID || snapshot.Generation != identity.Generation || snapshot.OwnerSessionID != "test-session" {
		t.Fatalf("tool attribution lost at process boundary: %+v", snapshot)
	}
}

func TestProcessOutputFailureIsAnErrorEvenAfterSuccessfulExit(t *testing.T) {
	code := 0
	result := processResult(process.Snapshot{ExitCode: &code, Error: "spool write failed"}, nil)
	if !result.IsError || result.Diagnostic.FaultDomain != "harness" || result.Diagnostic.Kind != "process_io" {
		t.Fatalf("output failure hidden by zero exit: %+v", result)
	}
}

func TestWriteStdinPipeAndPTY(t *testing.T) {
	tool := testExec(t, nil)
	pipe := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "sleep 120", "yield_time_ms": 250}))
	rejected := (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": pipe.SessionID, "chars": "ordinary input\n"})
	if !rejected.IsError {
		t.Fatal("pipe accepted ordinary input")
	}
	if _, err := tool.processes.Terminate(pipe.SessionID); err != nil {
		t.Fatal(err)
	}
	tty := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "read value; printf 'received:%s' \"$value\"", "tty": true, "yield_time_ms": 250}))
	input := (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": tty.SessionID, "chars": "hello\n", "yield_time_ms": 2000})
	end := execSnapshot(t, input)
	if input.IsError || end.Status != "exited" || !strings.Contains(end.Output, "received:hello") {
		t.Fatalf("PTY: %+v", input)
	}
}

func TestShellObservationArgumentValidation(t *testing.T) {
	execTool := execCommandTool{}
	writeTool := writeStdinTool{}
	for _, args := range []map[string]any{
		{"cmd": "true", "yield_time_ms": -1},
		{"cmd": "true", "max_output_tokens": 0},
		{"cmd": "true", "max_output_tokens": -1},
		{"cmd": "true", "yield_time_ms": json.Number("18446744073709551615")},
	} {
		if err := execTool.Validate(args); err == nil {
			t.Fatalf("exec accepted invalid observation argument: %+v", args)
		}
	}
	for _, args := range []map[string]any{
		{"session_id": 0},
		{"session_id": -1},
		{"session_id": int64(1 << 53)},
		{"session_id": nil},
		{"session_id": 1, "max_output_tokens": 0},
		{"session_id": 1, "yield_time_ms": -1},
	} {
		if err := writeTool.Validate(args); err == nil {
			t.Fatalf("write accepted invalid observation argument: %+v", args)
		}
	}
	// Zero yields clamp to the responsiveness floor; large positive yields
	// clamp before duration conversion, instead of being rejected globally.
	for _, yield := range []any{0, int64(1 << 62)} {
		if err := execTool.Validate(map[string]any{"cmd": "true", "yield_time_ms": yield}); err != nil {
			t.Fatalf("valid clamped yield rejected: %v", err)
		}
		if err := writeTool.Validate(map[string]any{"session_id": int64((1 << 53) - 1), "yield_time_ms": yield}); err != nil {
			t.Fatalf("valid clamped yield or handle rejected: %v", err)
		}
	}
}

func TestTTYGitDiffDoesNotEnterInheritedPager(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable for pager fixture")
	}
	t.Setenv("PAGER", "printf pager-launched; sleep 120")
	t.Setenv("GIT_PAGER", "printf pager-launched; sleep 120")
	t.Setenv("GH_PAGER", "printf pager-launched; sleep 120")
	t.Setenv("TERM", "xterm-256color")
	tool := testExec(t, nil)
	result := tool.Execute(t.Context(), map[string]any{
		"cmd": "git init --quiet && printf before > tracked && git add tracked && printf after > tracked && git --paginate diff",
		"tty": true, "login": false, "yield_time_ms": 250,
	})
	snapshot := execSnapshot(t, result)
	if result.IsError || snapshot.Status != "exited" || !strings.Contains(snapshot.Output, "+after") || strings.Contains(snapshot.Output, "pager-launched") {
		t.Fatalf("inherited pager parked agent terminal: %+v", snapshot)
	}
}

type fakeSpool struct {
	dir string
	err error
}

func (s fakeSpool) CreateOutputFile(string, string) (*os.File, error) {
	if s.err != nil {
		return nil, s.err
	}
	return os.CreateTemp(s.dir, "spool-*.log")
}

func TestShellProcessSpoolAndFallback(t *testing.T) {
	dir := t.TempDir()
	tool := testExec(t, fakeSpool{dir: dir})
	snap := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "printf spooled-out"}))
	if filepath.Dir(snap.OutputFile) != dir {
		t.Fatalf("output outside session spool: %s", snap.OutputFile)
	}
	raw, err := os.ReadFile(snap.OutputFile)
	if err != nil || string(raw) != "spooled-out" {
		t.Fatalf("spool: %q %v", raw, err)
	}
	fallback := testExec(t, fakeSpool{err: os.ErrPermission})
	res := fallback.Execute(t.Context(), map[string]any{"cmd": "printf fallback"})
	if res.IsError {
		t.Fatalf("spool refusal broke execution: %+v", res)
	}
	path := execSnapshot(t, res).OutputFile
	fallback.processes.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fallback log not cleaned: %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}
