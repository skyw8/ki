package search

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ToolsShimName is the shell fragment written next to the bundled binaries and
// sourced through BASH_ENV by the shell tools.
const ToolsShimName = "shim.sh"

var toolsDir = sync.OnceValues(resolveToolsDir)

// ToolsDir returns a directory that holds the embedded search executables (rg
// and, where available, fd) together with the BASH_ENV shim. The Bash and
// PowerShell tools put this directory on PATH so shell commands can use rg/fd
// regardless of what the host has installed.
//
// Binaries are materialized once per user and reused while their SHA-256 still
// matches the embedded bytes. When no writable cache directory exists the tools
// fall back to a process-lifetime temporary directory.
func ToolsDir() (string, error) {
	return toolsDir()
}

func resolveToolsDir() (string, error) {
	if len(embeddedBinaries()) == 0 {
		return "", errEmbeddedRGMissing
	}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		dir := filepath.Join(cache, "ki", "tools", runtime.GOOS+"-"+runtime.GOARCH)
		if err := materializeTools(dir); err == nil {
			return dir, nil
		}
	}

	// Why: a locked-down container may have no writable cache directory. A
	// process-lifetime temp dir still lets shell commands resolve rg/fd; unlike
	// the previous per-call temp file it is intentionally not removed, because
	// PATH points at it for as long as ki runs.
	dir, err := os.MkdirTemp("", "ki-tools-*")
	if err != nil {
		return "", fmt.Errorf("create temporary tools directory: %w", err)
	}
	if err := materializeTools(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// executable returns a stable rg executable path plus a cleanup function. The
// normal path is the shared tools directory, so the same binary backs both the
// Grep/Glob engines and shell commands.
func executable() (string, func(), error) {
	if truthy(os.Getenv("KI_USE_SYSTEM_RIPGREP")) {
		path, err := exec.LookPath("rg")
		if err != nil {
			return "", func() {}, fmt.Errorf("system ripgrep requested but unavailable: %w", err)
		}
		return path, func() {}, nil
	}

	data, name := embeddedRG()
	if len(data) == 0 {
		return "", func() {}, errEmbeddedRGMissing
	}
	dir, err := ToolsDir()
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, executableName(name))
	if _, statErr := os.Stat(path); statErr != nil {
		return "", func() {}, errEmbeddedRGMissing
	}
	return path, func() {}, nil
}

type embeddedBinary struct {
	name string
	data []byte
}

// embeddedBinaries lists the binaries embedded for the current target. fd is
// absent on unsupported platforms and rg may be requested from the system, so
// callers must tolerate either being missing.
func embeddedBinaries() []embeddedBinary {
	var out []embeddedBinary
	if data, name := embeddedRG(); len(data) > 0 {
		out = append(out, embeddedBinary{name: executableName(name), data: data})
	}
	if data, name := embeddedFD(); len(data) > 0 {
		out = append(out, embeddedBinary{name: executableName(name), data: data})
	}
	return out
}

func executableName(name string) string {
	if name == "" {
		name = "tool"
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	return name
}

func materializeTools(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create tools directory: %w", err)
	}
	for _, bin := range embeddedBinaries() {
		hash := sha256.Sum256(bin.data)
		digest := hex.EncodeToString(hash[:])
		if _, ok := materializeCached(dir, bin.name, bin.data, digest); !ok {
			return fmt.Errorf("materialize embedded %s", bin.name)
		}
	}
	return materializeShim(dir)
}

func materializeCached(dir, name string, data []byte, digest string) (string, bool) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false
	}
	destination := filepath.Join(dir, name)
	if fileMatchesDigest(destination, data, digest) {
		return destination, true
	}

	tmp, err := os.CreateTemp(dir, ".tool-*")
	if err != nil {
		return "", false
	}
	tmpPath := tmp.Name()
	if err := writeExecutable(tmp, data); err != nil {
		_ = os.Remove(tmpPath)
		return "", false
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		_ = os.Remove(tmpPath)
		if fileMatchesDigest(destination, data, digest) {
			return destination, true
		}
		return "", false
	}
	return destination, true
}

// toolsShim re-prepends the bundled tools directory and the extension PATH
// directories to PATH. bash -lc sources /etc/profile and the user's profile
// before BASH_ENV, and those files may reset PATH entirely (Debian's
// /etc/profile does), so exporting PATH on the child process alone is not
// enough. KI_ORIG_BASH_ENV chains to any BASH_ENV the user already had so the
// shim does not shadow it, and KI_EXTENSION_PATH_DIRS carries the directories
// contributed by enabled extensions: they cannot be baked into this file
// because it is one shared artifact whose content must not depend on the
// session.
const toolsShim = `# ki: expose bundled rg/fd to shell commands.
[ -n "${KI_ORIG_BASH_ENV:-}" ] && [ -r "$KI_ORIG_BASH_ENV" ] && . "$KI_ORIG_BASH_ENV"
_ki_tools_dir=$(cd "$(dirname "$BASH_SOURCE")" 2>/dev/null && pwd)
if [ -n "$_ki_tools_dir" ]; then
	case ":$PATH:" in
		*":$_ki_tools_dir:"*) ;;
		*) PATH="$_ki_tools_dir:$PATH"; export PATH ;;
	esac
fi
unset _ki_tools_dir
if [ -n "${KI_EXTENSION_PATH_DIRS:-}" ]; then
	_ki_old_ifs=$IFS
	IFS=:
	for _ki_dir in $KI_EXTENSION_PATH_DIRS; do
		[ -d "$_ki_dir" ] || continue
		case ":$PATH:" in
			*":$_ki_dir:"*) ;;
			*) PATH="$_ki_dir:$PATH"; export PATH ;;
		esac
	done
	IFS=$_ki_old_ifs
	unset _ki_old_ifs _ki_dir
fi
`

func materializeShim(dir string) error {
	path := filepath.Join(dir, ToolsShimName)
	if current, err := os.ReadFile(path); err == nil && string(current) == toolsShim {
		return nil
	}
	if err := os.WriteFile(path, []byte(toolsShim), 0o600); err != nil {
		return fmt.Errorf("write tools shim: %w", err)
	}
	return nil
}

func writeExecutable(file *os.File, data []byte) error {
	if err := file.Chmod(0o700); err != nil {
		return fmt.Errorf("set embedded executable permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write embedded executable: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync embedded executable: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close embedded executable: %w", err)
	}
	return nil
}

func fileMatchesDigest(path string, data []byte, expected string) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() != int64(len(data)) {
		return false
	}
	file, err := os.Open(path) //nolint:gosec // path is the Ki-managed cache destination
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	h := sha256.New()
	if _, err := file.WriteTo(h); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == expected
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
