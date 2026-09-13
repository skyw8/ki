package tools

import (
	"os/exec"
	"time"
)

// ProcessWaitDelay bounds Wait after killing a sidecar process group.
// Copied from shellWaitDelay so abort does not hang on grandchildren.
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
