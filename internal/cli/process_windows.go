//go:build windows

package cli

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr {
	// DETACHED_PROCESS is not exported by every Go Windows syscall version;
	// keep the documented Win32 bit here so the parent console is not reused.
	const detachedProcess = 0x00000008
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}
