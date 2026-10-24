//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

func detachCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func afterStart(cmd *exec.Cmd) {
	_ = cmd
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

func releaseCmd(_ *exec.Cmd) {}
