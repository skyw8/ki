//go:build !windows

package e2e

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func waitDetachedProcess(p *os.Process) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()

	for {
		err := p.Signal(syscall.Signal(0))
		if err != nil {
			if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return err
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("process %d is still alive", p.Pid)
		case <-poll.C:
		}
	}
}
