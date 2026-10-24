package process

import (
	"os/exec"
	"time"
)

const ProcessWaitDelay = 200 * time.Millisecond

func AttachProcessGroup(cmd *exec.Cmd) {
	detachCmd(cmd)
}

func AfterProcessStart(cmd *exec.Cmd) {
	afterStart(cmd)
}

func ReleaseProcessGroup(cmd *exec.Cmd) {
	releaseCmd(cmd)
}

func KillProcessGroup(cmd *exec.Cmd) {
	killCmd(cmd)
}

func SetWaitDelay(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.WaitDelay = ProcessWaitDelay
	}
}
