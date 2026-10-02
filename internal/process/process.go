package process

import (
	"os/exec"
	"time"
)

// ProcessWaitDelay bounds Wait after killing a sidecar process group.
// Descendants can retain output pipes after their launcher exits, so draining
// must stay bounded even when the process tree has already been terminated.
const ProcessWaitDelay = 200 * time.Millisecond

// AttachProcessGroup puts cmd in its own process group (Unix Setpgid /
// Windows CREATE_NEW_PROCESS_GROUP) so KillProcessGroup can reap children.
func AttachProcessGroup(cmd *exec.Cmd) {
	detachCmd(cmd)
}

// AfterProcessStart joins a started command to the platform kill group. Unix
// needs nothing (Setpgid covers the tree); Windows assigns a job object so
// KillProcessGroup reaps descendants of launchers whose processes are not
// linked by the parent pid the toolhelp snapshot reports.
func AfterProcessStart(cmd *exec.Cmd) {
	afterStart(cmd)
}

// ReleaseProcessGroup releases platform ownership after cmd.Wait. On Windows,
// closing the job also terminates any descendants left by an exited launcher.
func ReleaseProcessGroup(cmd *exec.Cmd) {
	releaseCmd(cmd)
}

// KillProcessGroup terminates cmd and its descendants.
func KillProcessGroup(cmd *exec.Cmd) {
	killCmd(cmd)
}

// SetWaitDelay applies ProcessWaitDelay to cmd.
func SetWaitDelay(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.WaitDelay = ProcessWaitDelay
	}
}
