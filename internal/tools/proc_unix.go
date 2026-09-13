//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

func detachCmd(cmd *exec.Cmd) {
	// Own process group so abort/timeout can kill grandchildren (find, pipelines),
	// not just the bash launcher. CommandContext only signals cmd.Process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func afterStart(cmd *exec.Cmd) {
	// Setpgid already makes the process group the unit of cancellation.
	_ = cmd
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
