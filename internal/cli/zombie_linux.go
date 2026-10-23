//go:build linux

package cli

import (
	"os"
	"strconv"
	"strings"
)

// exitedProcess reports whether pid has already terminated but is still
// visible as a zombie because its parent has not reaped it.
func exitedProcess(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") //nolint:gosec // fixed procfs path
	if err != nil {
		return false
	}
	// The command name is parenthesised and may contain spaces, so the state
	// is the first field after the closing parenthesis.
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 || i+2 > len(raw) {
		return false
	}
	fields := strings.Fields(string(raw[i+1:]))
	return len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
}
