package logging

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupAndClose(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)

	home := t.TempDir()
	closer, err := Setup(Options{Home: home, Level: "info", Role: "test", MaxSizeMB: 1, MaxBackups: 3})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	slog.Info("test log", "key", "value")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	path := filepath.Join(home, "ki.log")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("log mode = %o, want 600", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !strings.Contains(string(b), "test log") {
		t.Fatalf("log file does not contain test record: %q", b)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		if record["role"] != "test" || record["pid"] == nil {
			t.Fatalf("missing common fields: %#v", record)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestSetupFailureDoesNotInstallLogger(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)

	badHome := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badHome, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(Options{Home: badHome, Level: "info", MaxSizeMB: 1, MaxBackups: 1}); err == nil {
		t.Fatal("Setup() error = nil, want error")
	}
	if slog.Default() != old {
		t.Fatal("Setup() changed the default logger on failure")
	}
}

func TestSetupRejectsInvalidLevel(t *testing.T) {
	if _, err := Setup(Options{Home: t.TempDir(), Level: "debgu", MaxSizeMB: 1, MaxBackups: 1}); err == nil {
		t.Fatal("Setup() error = nil, want invalid level error")
	}
}

func TestSensitiveAttributesAreRedacted(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)

	home := t.TempDir()
	closer, err := Setup(Options{Home: home, Level: "info", Role: "test", MaxSizeMB: 1, MaxBackups: 1})
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("sensitive", "api_key", "secret-value", "session_id", "safe")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "ki.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-value") || !strings.Contains(string(b), "[REDACTED]") {
		t.Fatalf("sensitive value was not redacted: %s", b)
	}
}

func TestLogRotation(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)

	home := t.TempDir()
	closer, err := Setup(Options{Home: home, Level: "info", Role: "test", MaxSizeMB: 1, MaxBackups: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12000; i++ {
		slog.Info("rotation", "payload", strings.Repeat("x", 120))
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".1", ".2"} {
		if _, err := os.Stat(filepath.Join(home, "ki.log"+suffix)); err != nil {
			t.Fatalf("missing rotated log %q: %v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "ki.log.3")); !os.IsNotExist(err) {
		t.Fatalf("unexpected log beyond max_backups: %v", err)
	}
}

func TestRecoverLogsPanicAndStack(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)

	home := t.TempDir()
	closer, err := Setup(Options{Home: home, Level: "info", Role: "test", MaxSizeMB: 1, MaxBackups: 1})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer Recover("panic boundary", "session_id", "sess-1")
		panic("boom")
	}()
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "ki.log"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "panic boundary" || record["panic"] != "boom" {
		t.Fatalf("panic record: %#v", record)
	}
	stack, ok := record["stack"].(string)
	if !ok || !strings.Contains(stack, "TestRecoverLogsPanicAndStack") {
		t.Fatalf("missing stack: %#v", record["stack"])
	}
}
