package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

const redacted = "[REDACTED]"

var (
	errMaxSize         = errors.New("log max_size_mb must be positive")
	errMaxBackups      = errors.New("log max_backups must be positive")
	errInvalidLogLevel = errors.New("invalid log level")
)

// Recover logs a recovered panic with its stack. It is intended for deferred
// boundary handlers; callers should decide whether the surrounding operation
// can safely continue after recovery.
func Recover(message string, attrs ...any) bool {
	if value := recover(); value != nil {
		attrs = append(attrs, "panic", fmt.Sprint(value), "stack", string(debug.Stack()))
		slog.Error(message, attrs...)
		return true
	}
	return false
}

// Options controls process logging.
type Options struct {
	Home  string
	Level string
	// Role identifies the process role in shared JSONL logs, for example
	// "client", "server", or "test". It is a log field, not a permission.
	Role       string
	MaxSizeMB  int
	MaxBackups int
}

type rotatingFile struct {
	mu         sync.Mutex
	once       sync.Once
	file       *os.File
	path       string
	lock       *flock.Flock
	maxBytes   int64
	maxBackups int
	closeErr   error
}

func newRotatingFile(path, lockPath string, maxBytes int64, maxBackups int) (*rotatingFile, error) {
	// path is constructed from the configured Ki home and never from request data.
	//nolint:gosec // the logger must open the configured absolute log path
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &rotatingFile{
		file:       file,
		path:       path,
		lock:       flock.New(lockPath),
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
	}, nil
}

func (f *rotatingFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.lock.Lock(); err != nil {
		return 0, fmt.Errorf("lock log file: %w", err)
	}
	defer func() { _ = f.lock.Unlock() }()

	if err := f.reopen(); err != nil {
		return 0, err
	}
	if err := f.rotateIfNeededLocked(len(p)); err != nil {
		return 0, err
	}
	n, writeErr := f.file.Write(p)
	closeErr := f.file.Close()
	f.file = nil
	return n, errors.Join(writeErr, closeErr)
}

func (f *rotatingFile) rotateIfNeededLocked(incoming int) error {
	if f.maxBytes <= 0 {
		return nil
	}
	info, err := f.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 || info.Size()+int64(incoming) <= f.maxBytes {
		return nil
	}

	if err := f.file.Close(); err != nil {
		return err
	}
	f.file = nil
	for i := f.maxBackups - 1; i >= 1; i-- {
		old := f.path + "." + strconv.Itoa(i)
		next := f.path + "." + strconv.Itoa(i+1)
		if err := removeIfExists(next); err != nil {
			return f.reopenAfterRotationError(err)
		}
		if err := os.Rename(old, next); err != nil && !os.IsNotExist(err) {
			return f.reopenAfterRotationError(err)
		}
	}
	if err := removeIfExists(f.path + ".1"); err != nil {
		return f.reopenAfterRotationError(err)
	}
	if err := os.Rename(f.path, f.path+".1"); err != nil {
		return f.reopenAfterRotationError(err)
	}
	return f.reopen()
}

func (f *rotatingFile) reopenAfterRotationError(rotationErr error) error {
	if err := f.reopen(); err != nil {
		return errors.Join(rotationErr, err)
	}
	return rotationErr
}

func (f *rotatingFile) reopen() error {
	if f.file != nil {
		if err := f.file.Close(); err != nil {
			return err
		}
		f.file = nil
	}
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	f.file = file
	return nil
}

func (f *rotatingFile) Close() error {
	f.once.Do(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if err := f.lock.Lock(); err != nil {
			f.closeErr = err
			return
		}
		defer func() { _ = f.lock.Unlock() }()
		if f.file != nil {
			f.closeErr = f.file.Close()
		}
	})
	return f.closeErr
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Setup installs the default JSONL slog logger and returns the log file closer.
// Callers must close the returned handle when the process is shutting down.
func Setup(opts Options) (io.Closer, error) {
	home := opts.Home
	if home == "" {
		home = os.Getenv("KI_HOME")
	}
	// home is the configured Ki home, not an attacker-controlled request path.
	//nolint:gosec // create the configured logging directory
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	if os.Getenv("KI_DEBUG") == "1" {
		level = slog.LevelDebug
	}
	if opts.MaxSizeMB <= 0 {
		return nil, errMaxSize
	}
	if opts.MaxBackups <= 0 {
		return nil, errMaxBackups
	}
	logFile, err := newRotatingFile(
		filepath.Join(home, "ki.jsonl"),
		filepath.Join(home, "ki.jsonl.lock"),
		int64(opts.MaxSizeMB)*1024*1024,
		opts.MaxBackups,
	)
	if err != nil {
		return nil, err
	}
	h := slog.NewJSONHandler(io.MultiWriter(os.Stderr, logFile), &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceAttr,
	})
	role := opts.Role
	if role == "" {
		role = "unknown"
	}
	slog.SetDefault(slog.New(h).With("pid", os.Getpid(), "role", role))
	return logFile, nil
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%w: %s", errInvalidLogLevel, strconv.Quote(raw))
	}
}

func replaceAttr(_ []string, attr slog.Attr) slog.Attr {
	key := strings.ToLower(attr.Key)
	if sensitiveKey(key) {
		return slog.String(attr.Key, redacted)
	}
	return attr
}

func sensitiveKey(key string) bool {
	if key == "api_key" || key == "apikey" || key == "authorization" ||
		key == "token" || key == "password" || key == "secret" || key == "private_key" ||
		key == "headers" || key == "prompt" || key == "body" || key == "content" {
		return true
	}
	return strings.HasSuffix(key, "_token") || strings.HasSuffix(key, "_secret") || strings.HasSuffix(key, "_key")
}
