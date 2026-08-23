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

const embeddedRGVersion = "15.2.0"

var materializeMu sync.Mutex

// executable returns a stable executable path and a cleanup function. The
// normal path is a per-user cache so repeated searches do not rewrite rg. A
// temporary fallback keeps the tool usable when the cache directory is not
// writable (for example in a locked-down CI container).
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
	if name == "" {
		name = "rg"
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}

	materializeMu.Lock()
	defer materializeMu.Unlock()
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		dir := filepath.Join(cache, "ki", "rg", embeddedRGVersion, runtime.GOOS+"-"+runtime.GOARCH)
		if path, ok := materializeCached(dir, name, data, digest); ok {
			return path, func() {}, nil
		}
	}

	file, err := os.CreateTemp("", "ki-rg-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary ripgrep: %w", err)
	}
	path := file.Name()
	if err := writeExecutable(file, data); err != nil {
		_ = os.Remove(path)
		return "", func() {}, err
	}
	if runtime.GOOS == "windows" {
		path += ".exe"
		if err := os.Rename(file.Name(), path); err != nil {
			_ = os.Remove(file.Name())
			_ = os.Remove(path)
			return "", func() {}, fmt.Errorf("name temporary ripgrep: %w", err)
		}
	}
	cleanup := func() { _ = os.Remove(path) }
	return path, cleanup, nil
}

func materializeCached(dir, name string, data []byte, digest string) (string, bool) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false
	}
	destination := filepath.Join(dir, name)
	if fileMatchesDigest(destination, data, digest) {
		return destination, true
	}

	tmp, err := os.CreateTemp(dir, ".rg-*")
	if err != nil {
		return "", false
	}
	tmpPath := tmp.Name()
	if err := writeExecutable(tmp, data); err != nil {
		_ = tmp.Close()
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

func writeExecutable(file *os.File, data []byte) error {
	if err := file.Chmod(0o700); err != nil {
		return fmt.Errorf("set ripgrep permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write embedded ripgrep: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync embedded ripgrep: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close embedded ripgrep: %w", err)
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
