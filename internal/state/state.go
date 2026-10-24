package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrNewerVersion = errors.New("unsupported")

type Migration func(raw []byte) ([]byte, error)

type header struct {
	Version int `json:"version"`
}

func Version(raw []byte) (int, error) {
	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return 0, err
	}
	return h.Version, nil
}

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
