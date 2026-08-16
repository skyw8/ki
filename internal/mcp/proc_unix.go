//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

func detachCmd(cmd *exec.Cmd) {
	// Own process group so killpg reaches npx grandchildren (node), not just the launcher.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
