package klog

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Setup installs the default slog logger. Secrets must never be logged by callers.
func Setup(home, level string) (*slog.Logger, error) {
	if home == "" {
		home = os.Getenv("KI_HOME")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home, "ki.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	w := io.MultiWriter(os.Stderr, f)
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	if os.Getenv("KI_DEBUG") == "1" {
		lv = slog.LevelDebug
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv})
	log := slog.New(h)
	slog.SetDefault(log)
	return log, nil
}
