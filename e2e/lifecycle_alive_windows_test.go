//go:build windows

package e2e

import "syscall"

func pidAlive(pid int) bool {
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
