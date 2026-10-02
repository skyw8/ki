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
	// Exit and output-drain timeout share ownership. ConPTY's native Close is
	// not idempotent, so these paths must never close the console handle twice.
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
	// ConPTY retains output ownership until ClosePseudoConsole. Its close path
	// is needed even for successful ordinary exits, so lack of EOF alone is not
	// an incomplete-output diagnostic (unlike a Unix PTY's EIO/EOF).
	return nil
}
