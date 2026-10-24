//go:build windows

package process

import (
	"errors"
	"os"
	"sync"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

type ownedTerminal struct {
	pty.Pty
	closeOnce sync.Once
	closeErr  error
}

func prepareTerminal(terminal pty.Pty) (pty.Pty, error) {
	return &ownedTerminal{Pty: terminal}, nil
}

func (p *ownedTerminal) Close() error {
	p.closeOnce.Do(func() { p.closeErr = p.Pty.Close() })
	return p.closeErr
}

func terminalReadError(err error) error {
	if errors.Is(err, os.ErrClosed) || errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		return nil
	}
	return err
}

func terminalDrainTimeoutError() error {
	return nil
}
