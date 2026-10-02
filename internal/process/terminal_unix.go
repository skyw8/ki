//go:build !windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

type pollableTerminal struct {
	pty.UnixPty
	ioFile    *os.File
	closeOnce sync.Once
	closeErr  error
}

func prepareTerminal(terminal pty.Pty) (pty.Pty, error) {
	native, ok := terminal.(pty.UnixPty)
	if !ok {
		return terminal, nil
	}
	fd := -1
	var setupErr error
	if err := native.Control(func(raw uintptr) {
		// Atomic CLOEXEC prevents unrelated concurrently spawned children from
		// inheriting the terminal master and keeping this session alive.
		fd, setupErr = unix.FcntlInt(raw, unix.F_DUPFD_CLOEXEC, 0)
		if setupErr == nil {
			setupErr = unix.SetNonblock(fd, true)
		}
	}); err != nil {
		return nil, err
	}
	if setupErr != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return nil, setupErr
	}
	// PTY setup calls File.Fd(), which switches Go's original descriptor to
	// blocking I/O. A fresh nonblocking descriptor restores runtime polling so
	// Close can release a write blocked behind a process that never reads stdin.
	file := os.NewFile(uintptr(fd), native.Master().Name())
	return &pollableTerminal{UnixPty: native, ioFile: file}, nil
}

func (p *pollableTerminal) Read(data []byte) (int, error) { return p.ioFile.Read(data) }

func (p *pollableTerminal) Write(data []byte) (int, error) { return p.ioFile.Write(data) }

func (p *pollableTerminal) Close() error {
	p.closeOnce.Do(func() { p.closeErr = errors.Join(p.ioFile.Close(), p.UnixPty.Close()) })
	return p.closeErr
}

func terminalReadError(err error) error {
	// Unix terminals report EIO, rather than EOF, after the last slave closes.
	// Closing the owned pollable descriptor also releases canceled blocked I/O.
	if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func terminalDrainTimeoutError() error {
	// Just like exec.Cmd.WaitDelay for pipes, forced Unix PTY closure is
	// incomplete output, not a successful silent drain.
	return exec.ErrWaitDelay
}
