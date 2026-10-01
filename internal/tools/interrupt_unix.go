//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

func interruptProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errTaskNotRunning
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}
