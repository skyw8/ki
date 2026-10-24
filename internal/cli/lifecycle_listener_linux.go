//go:build linux

package cli

import (
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func listenerPID(addr string) int {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return 0
	}
	inodes := map[string]struct{}{}
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, readErr := os.ReadFile(name) //nolint:gosec // fixed procfs paths
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, localPort, ok := splitProcAddr(fields[1])
			if !ok || localPort != port {
				continue
			}
			inodes[fields[9]] = struct{}{}
		}
	}
	if len(inodes) == 0 {
		return 0
	}
	_ = host
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, fdErr := os.ReadDir(fdDir)
		if fdErr != nil {
			continue
		}
		for _, fd := range fds {
			target, linkErr := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if linkErr != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if _, ok := inodes[inode]; ok {
				return pid
			}
		}
	}
	return 0
}

func splitProcAddr(field string) (host string, port int, ok bool) {
	parts := strings.Split(field, ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	raw, err := hex.DecodeString(parts[0])
	if err != nil || len(raw) != 4 {
		return "", 0, false
	}
	p, err := strconv.ParseInt(parts[1], 16, 32)
	if err != nil {
		return "", 0, false
	}
	return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String(), int(p), true
}
