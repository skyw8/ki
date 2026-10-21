//go:build windows

package cli

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddrInUse reports a port conflict. WSAEADDRINUSE is a distinct Errno from
// the invented syscall.EADDRINUSE that package syscall defines on Windows, so
// matching the Winsock constant is the only reliable check there.
func isAddrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
