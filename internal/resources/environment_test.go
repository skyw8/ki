package resources

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadEnvironment(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	home := filepath.Join(t.TempDir(), "ki")
	cwd := filepath.Join(t.TempDir(), "work")
	env := loadEnvironment(home, cwd, now)
	if env.KIHome != filepath.ToSlash(home) || env.CWD != filepath.ToSlash(cwd) {
		t.Fatalf("paths: %+v", env)
	}
	if env.Date != "2026-08-15" || env.Timezone != "CST (UTC+8)" {
		t.Fatalf("time: %+v", env)
	}
	if env.OS == "" || env.Architecture == "" {
		t.Fatalf("runtime: %+v", env)
	}
}

func TestDetectOS(t *testing.T) {
	readFiles := func(files map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			value, ok := files[path]
			if !ok {
				return nil, os.ErrNotExist
			}
			return []byte(value), nil
		}
	}

	tests := []struct {
		name  string
		goos  string
		env   map[string]string
		files map[string]string
		want  string
	}{
		{name: "macOS", goos: "darwin", want: "macOS"},
		{name: "Windows", goos: "windows", want: "Windows"},
		{name: "Linux", goos: "linux", want: "Linux"},
		{name: "WSL environment marker", goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, want: "WSL"},
		{name: "WSL kernel marker", goos: "linux", files: map[string]string{"/proc/sys/kernel/osrelease": "5.15.90.1-microsoft-standard-WSL2"}, want: "WSL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			if got := detectOSWith(tt.goos, getenv, readFiles(tt.files)); got != tt.want {
				t.Fatalf("detectOSWith() = %q, want %q", got, tt.want)
			}
		})
	}
}
