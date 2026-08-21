//go:build windows

package search

import (
	"os/exec"
	"strconv"
	"syscall"
)

func detachRGCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killRGCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Process.Kill only terminates rg; taskkill /T is needed when rg has
	// delegated traversal or inherited a pipe through a child process.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
