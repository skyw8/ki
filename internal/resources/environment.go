package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Environment is runtime metadata captured with a session's resource snapshot.
type Environment struct {
	KIHome       string
	CWD          string
	OS           string
	Architecture string
	Date         string
	Timezone     string
}

func loadEnvironment(home, cwd string, now time.Time) Environment {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	_, offset := now.Zone()
	return Environment{
		KIHome:       filepath.ToSlash(home),
		CWD:          filepath.ToSlash(cwd),
		OS:           detectOS(),
		Architecture: runtime.GOARCH,
		Date:         now.Format("2006-01-02"),
		Timezone:     fmt.Sprintf("%s (UTC%+d)", now.Format("MST"), offset/3600),
	}
}

func detectOS() string {
	return detectOSWith(runtime.GOOS, os.Getenv, os.ReadFile)
}

func detectOSWith(goos string, getenv func(string) string, readFile func(string) ([]byte, error)) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		if isWSL(getenv, readFile) {
			return "WSL"
		}
		return "Linux"
	default:
		return goos
	}
}

func isWSL(getenv func(string) string, readFile func(string) ([]byte, error)) bool {
	// WSL runs a Linux userspace, so runtime.GOOS alone cannot distinguish it
	// from native Linux. Prefer the process markers, then inspect the kernel
	// identity as a fallback for services that start with a reduced environment.
	if strings.TrimSpace(getenv("WSL_DISTRO_NAME")) != "" ||
		strings.TrimSpace(getenv("WSL_INTEROP")) != "" {
		return true
	}

	for _, path := range []string{"/proc/sys/kernel/osrelease", "/proc/version"} {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		kernel := strings.ToLower(string(data))
		if strings.Contains(kernel, "microsoft") || strings.Contains(kernel, "wsl") {
			return true
		}
	}
	return false
}
