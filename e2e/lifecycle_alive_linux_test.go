//go:build linux

package e2e

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	raw, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if readErr != nil {
		return true
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 || i+2 > len(raw) {
		return true
	}
	fields := strings.Fields(string(raw[i+1:]))
	return len(fields) == 0 || (fields[0] != "Z" && fields[0] != "X")
}
