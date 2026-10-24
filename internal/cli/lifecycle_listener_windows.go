//go:build windows

package cli

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
)

func listenerPID(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	if _, convErr := strconv.Atoi(port); convErr != nil {
		return 0
	}
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		_, localPort, splitErr := net.SplitHostPort(fields[1])
		if splitErr != nil || localPort != port {
			continue
		}
		if pid, convErr := strconv.Atoi(fields[4]); convErr == nil && pid > 0 {
			return pid
		}
	}
	return 0
}
