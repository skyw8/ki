package process

import (
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
