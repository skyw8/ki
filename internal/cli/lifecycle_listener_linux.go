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

// listenerPID finds the process listening on addr by matching the socket inode
// in /proc/net/tcp{,6} against the open file descriptors under /proc.
//
// Why this exists: server.json written by a build without the pid field has no
// other way to identify its daemon. Without it, the first `ki stop` after an
// upgrade could only explain the problem instead of fixing it, and stale
// `KI_HOME=/tmp/...` daemons would keep ports open forever.
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
			// sl local_address rem_address st ... inode is field 9.
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
	// A wildcard listener is reachable through loopback, and dialAddress has
	// already rewritten it to 127.0.0.1, so no extra host filtering is needed.
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

// splitProcAddr decodes the "0100007F:4D4C" form used by /proc/net/tcp.
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
	// /proc stores the address in network byte order per 32-bit word, which
	// for 127.0.0.1 reads as 0100007F.
	return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String(), int(p), true
}
