//go:build windows

package process

import (
	"sync"

	pty "github.com/aymanbagabas/go-pty"
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
