//go:build windows

package mcp

import "os/exec"

func detachCmd(cmd *exec.Cmd) {}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
