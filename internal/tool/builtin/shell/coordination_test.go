package shelltools

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBlockedTTYInputObserverCanCancelWithoutKillingProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stty fixture is Unix-only; ConPTY input has separate platform coverage")
	}
	tool := testExec(t, nil)
	snapshot := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "stty -icanon -echo; sleep 120", "tty": true, "yield_time_ms": 250}))
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	value, err := tool.processes.Interact(ctx, snapshot.SessionID, strings.Repeat("x", 1024*1024), time.Second, 100, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || value.Status != "running" {
		t.Fatalf("blocked input did not release observer: %+v %v (%s)", value, err, time.Since(started))
	}
	if _, err := tool.processes.Terminate(snapshot.SessionID); err != nil {
		t.Fatal(err)
	}
	// Termination unblocks the manager-owned write and releases its input lock.
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	if value, err := tool.processes.Interact(ctx2, snapshot.SessionID, "", time.Millisecond, 100, nil); err != nil || value.Status != "exited" {
		t.Fatalf("input ownership leaked: %+v %v", value, err)
	}
}
