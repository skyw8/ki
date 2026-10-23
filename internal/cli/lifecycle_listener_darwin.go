//go:build darwin

package cli

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// listenerPID finds the process listening on addr with lsof, because Darwin has
// no procfs. A missing lsof yields 0 (unknown), which callers treat as "cannot
// identify the daemon" rather than as "nothing is running".
func listenerPID(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	if _, convErr := strconv.Atoi(port); convErr != nil {
		return 0
	}
	//nolint:gosec // fixed arguments; port is validated as numeric above.
	out, err := exec.Command("lsof", "-nP", "-ti", "tcp:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Fields(string(out)) {
		if pid, convErr := strconv.Atoi(line); convErr == nil && pid > 0 {
			return pid
		}
	}
	return 0
}
