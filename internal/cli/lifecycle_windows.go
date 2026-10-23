//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// stopSignals is limited to the console interrupt on Windows: there is no
// SIGTERM, and the portable way to stop a detached daemon here is
// POST /v1/shutdown (or taskkill for a pre-lifecycle build).
func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// processAlive opens the process and asks for its exit code; Windows keeps the
// pid reserved while the handle is closed but the process is still running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const (
		queryLimitedInformation = 0x1000
		stillActive             = 259
	)
	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// terminateProcess asks taskkill for a normal close. A console app has no
// window to close, so Windows usually ends it outright; that is still the
// documented pre-force step, and forceKillProcess is the guaranteed one.
func terminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	//nolint:gosec // pid comes from server.json or the listener table.
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill %d: %w: %s", pid, err, out)
	}
	return nil
}

func forceKillProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
