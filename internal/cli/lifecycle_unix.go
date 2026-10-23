//go:build unix

package cli

import (
	"errors"
	"os"
	"syscall"
)

// stopSignals are the signals that should end a server through owned cleanup.
// SIGINT covers Ctrl+C in a foreground terminal; SIGTERM covers `kill` and the
// service managers scripts/run.sh relies on.
func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// processAlive reports whether pid is still running. A permission error means
// the process exists but belongs to another user, which still blocks a restart.
//
// Why the zombie check: signal 0 succeeds for an exited child that its parent
// has not reaped yet, and a daemon started from a script or a test runner is
// exactly that child. Treating a zombie as alive would make `ki stop` wait out
// its whole timeout on a server that already finished shutdown.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	return !exitedProcess(pid)
}

func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func forceKillProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
