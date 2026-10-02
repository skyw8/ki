//go:build !windows

package process

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

func TestPollableTerminalDescriptorIsNonblockingAndCloseOnExec(t *testing.T) {
	native, err := pty.New()
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	terminal, err := prepareTerminal(native)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	p, ok := terminal.(*pollableTerminal)
	if !ok {
		t.Fatalf("unexpected Unix PTY wrapper %T", terminal)
	}
	conn, err := p.ioFile.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var fdFlags, statusFlags int
	var flagErr error
	if err := conn.Control(func(fd uintptr) {
		fdFlags, flagErr = unix.FcntlInt(fd, unix.F_GETFD, 0)
		if flagErr == nil {
			statusFlags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if flagErr != nil || fdFlags&unix.FD_CLOEXEC == 0 || statusFlags&unix.O_NONBLOCK == 0 {
		t.Fatalf("unsafe PTY descriptor flags: fd=%x status=%x err=%v", fdFlags, statusFlags, flagErr)
	}
}

func TestTerminalReadErrorDistinguishesNormalCloseFromFailure(t *testing.T) {
	for _, err := range []error{nil, fmt.Errorf("terminal EOF: %w", syscall.EIO), os.ErrClosed} {
		if got := terminalReadError(err); got != nil {
			t.Fatalf("normal terminal close treated as failure: %v", got)
		}
	}
	failure := errors.New("unexpected terminal failure")
	if got := terminalReadError(failure); !errors.Is(got, failure) {
		t.Fatalf("unexpected terminal failure hidden: %v", got)
	}
}
