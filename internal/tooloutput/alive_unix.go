//go:build unix

package tooloutput

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid is still running. known is false when the
// platform or the error cannot answer, in which case callers must fall back to
// the recorded heartbeat instead of deleting a directory that may be in use.
func processAlive(pid int) (alive bool, known bool) {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, syscall.EPERM):
		// The process exists but belongs to another user.
		return true, true
	case errors.Is(err, syscall.ESRCH):
		return false, true
	default:
		return false, false
	}
}
