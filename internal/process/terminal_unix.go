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
	if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func terminalDrainTimeoutError() error {
	return exec.ErrWaitDelay
}
