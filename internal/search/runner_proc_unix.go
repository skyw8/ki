//go:build !windows

package search

import (
	"os/exec"
	"syscall"
)

func detachRGCommand(cmd *exec.Cmd) {
	// Own a process group so timeout/cancel also terminates ripgrep's children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killRGCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
