//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
