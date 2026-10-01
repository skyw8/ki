//go:build windows

package tools

import (
	"golang.org/x/sys/windows"
	"os/exec"
)

func interruptProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errTaskNotRunning
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}
