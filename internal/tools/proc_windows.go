//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
)

func detachCmd(cmd *exec.Cmd) {
	// New process group so the serve console's Ctrl+C is not the only
	// cancellation path; abort still uses taskkill on the tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killCmd(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Process.Kill is TerminateProcess: bash dies, find/sleep children keep
	// the stdout pipe and Wait hangs. /T walks the tree by parent pid.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
