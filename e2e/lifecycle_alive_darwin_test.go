//go:build darwin

package e2e

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// zombieState is Darwin's P_stat for an exited, unreaped process. x/sys/unix
// does not export SZOMB there, and the value is stable ABI.
const zombieState = 5

// pidAlive mirrors the CLI's liveness probe. Darwin answers signal 0 for a
// zombie, so the state has to be read separately: the daemon this test just
// stopped stays unreaped until the test's own cleanup waits for it.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil {
		return true
	}
	return info.Proc.P_stat != zombieState
}
