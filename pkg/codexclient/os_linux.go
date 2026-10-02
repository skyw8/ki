package codexclient

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func hostOS() (string, string) {
	// os_info uses distribution metadata on Linux, not the kernel release.
	if raw, err := os.ReadFile("/etc/os-release"); err == nil {
		values := make(map[string]string)
		for line := range strings.SplitSeq(string(raw), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = strings.Trim(value, `"'`)
			}
		}
		name := values["NAME"]
		if strings.Contains(name, "Debian") {
			name = "Debian"
		}
		if name != "" && values["VERSION_ID"] != "" {
			return name, values["VERSION_ID"]
		}
	}
	var name unix.Utsname
	if unix.Uname(&name) == nil {
		return "Linux", unix.ByteSliceToString(name.Release[:])
	}
	return "Linux", "unknown"
}
