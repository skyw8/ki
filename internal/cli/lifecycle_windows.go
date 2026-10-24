//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

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
