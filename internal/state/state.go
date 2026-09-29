package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrNewerVersion reports that a document was written by a schema newer than
// the running build understands. Callers fail fast or fall back, but never
// rewrite the document: dropping fields the build cannot see would be silent
// data loss.
var ErrNewerVersion = errors.New("unsupported")

// Migration rewrites a document from one schema version to the next.
type Migration func(raw []byte) ([]byte, error)

type header struct {
	Version int `json:"version"`
}

// Version reads the schema version header of a JSON document. A document
// without a header is version 0, which callers treat as current (hand-written
// or pre-version).
func Version(raw []byte) (int, error) {
	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return 0, err
	}
	return h.Version, nil
}

// Migrate returns raw rewritten as current. A missing (0) or already-current
// version is returned unchanged. An older version is walked through steps[from]
// until it reaches current; a missing step is an error. A newer version returns
// ErrNewerVersion, and is never downgraded.
func Migrate(raw []byte, current int, steps map[int]Migration) (out []byte, migrated bool, err error) {
	v, err := Version(raw)
	if err != nil {
		return nil, false, err
	}
	switch {
	case v == 0 || v == current:
		return raw, false, nil
	case v > current:
		return nil, false, newerVersion(v, current)
	}
	out = raw
	for v < current {
		step, ok := steps[v]
		if !ok {
			return nil, false, fmt.Errorf("version %d to %d: %w", v, v+1, errNoMigration)
		}
		out, err = step(out)
		if err != nil {
			return nil, false, fmt.Errorf("migrate version %d to %d: %w", v, v+1, err)
		}
		migrated = true
		v++
	}
	return out, migrated, nil
}

// ReadFile reads and migrates a versioned document. A missing file returns
// os.ErrNotExist unchanged. migrated reports whether a step ran; callers keep
// the migrated bytes in memory and persist them on their next write.
func ReadFile(path string, current int, steps map[int]Migration) (raw []byte, migrated bool, err error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is a Ki-managed state file
	if err != nil {
		return nil, false, err
	}
	out, migrated, err := Migrate(b, current, steps)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return out, migrated, nil
}

// WriteJSON atomically replaces path with indented JSON: a same-directory temp
// file is written, synced and renamed over the target, so a concurrent reader
// never observes a half-written document.
func WriteJSON(path string, v any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err = f.Chmod(perm); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(tmp, path)
}

// WriteVersioned is WriteJSON that refuses to overwrite a document written by a
// newer schema, surfacing ErrNewerVersion instead of clobbering fields the
// build cannot see. When the existing document is an older schema it is backed
// up first, so a forward rewrite keeps a recoverable copy.
func WriteVersioned(path string, current int, v any, perm os.FileMode) error {
	b, err := os.ReadFile(path) //nolint:gosec // path is a Ki-managed state file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		ver, verr := Version(b)
		if verr != nil {
			return fmt.Errorf("%s: %w", path, verr)
		}
		if ver > current {
			return fmt.Errorf("%s: %w", path, newerVersion(ver, current))
		}
		if ver != 0 && ver != current {
			if _, berr := Backup(path); berr != nil {
				return berr
			}
		}
	}
	return WriteJSON(path, v, perm)
}

// Backup copies path to a sibling "path.bak-<UTC timestamp>" and returns the
// new path. A missing source returns "" and no error.
func Backup(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is a Ki-managed state file
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	dst := path + ".bak-" + time.Now().UTC().Format("20060102T150405")
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}

var errNoMigration = errors.New("no migration")

func newerVersion(found, supported int) error {
	return fmt.Errorf("version %d is %w; this build supports version %d (upgrade ki)", found, ErrNewerVersion, supported)
}
