//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// isAddrInUse reports a port conflict. Windows needs its own check: the
// platform reports WSAEADDRINUSE (10048), which errors.Is does not match
// against syscall.EADDRINUSE there.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
