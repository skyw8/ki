package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessCapacityAndSessionIsolation(t *testing.T) {
	shells := DiscoverShellRuntime()
	if !shells.BashAvailable() {
		t.Skip("Bash unavailable for POSIX command fixtures")
	}
	shell, err := ResolveShell(shells.WithoutPowerShell(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	manager.limit = 1
	t.Cleanup(manager.Close)
	cwd := t.TempDir()
	id, err := manager.Start(t.Context(), shell, cwd, "sleep 120", false, Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(t.Context(), shell, cwd, "printf no", false, Identity{}); err == nil {
		t.Fatal("live capacity was not enforced")
	}
	other := NewManager()
	defer other.Close()
	if _, err := other.Interact(t.Context(), id, "", time.Millisecond, 10, nil); err == nil {
		t.Fatal("another session accessed process")
	}
	if _, err := manager.Terminate(id); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(t.Context(), shell, cwd, "printf yes", false, Identity{}); err != nil {
		t.Fatal("finished process still occupies live capacity")
	}
}

func TestProcessRetentionDoesNotReclaimBeforeFinalPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unpublished-output")
	if err := os.WriteFile(path, []byte("output"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	for id := int64(1); id <= 256; id++ {
		// Completed but unread terminals must remain retained.
		manager.processes[id] = &shellProcess{snapshot: Snapshot{Status: "exited"}, total: 1}
	}
	p := manager.processes[1]
	p.total = 0
	p.temporary = true
	p.snapshot.OutputFile = path
	p.publisher = newProcessPublisher(func(Update) {})
	// Capacity is checked before the fake shell path can be executed.
	_, err := manager.Start(t.Context(), Shell{path: "unavailable-test-shell"}, t.TempDir(), "true", false, Identity{})
	if err == nil || !strings.Contains(err.Error(), "retention capacity") {
		t.Fatalf("unpublished terminal was reclaimed: %v", err)
	}
	if manager.processes[1] != p {
		t.Fatal("unpublished handle was removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unpublished spool was removed: %v", err)
	}
}
