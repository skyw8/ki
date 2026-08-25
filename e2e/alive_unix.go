//go:build !windows

package e2e

import "syscall"

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
