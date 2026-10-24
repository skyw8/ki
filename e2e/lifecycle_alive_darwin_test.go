//go:build darwin

package e2e

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

const zombieState = 5

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
