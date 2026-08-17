package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"ki/internal/idgen"
)

const version = 1

// Record is one registered workspace.
type Record struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Title      string   `json:"title"`
	CreatedAt  string   `json:"createdAt"`
	UpdatedAt  string   `json:"updatedAt"`
	SessionIDs []string `json:"sessionIds,omitempty"`
}

type fileData struct {
	Version      int      `json:"version"`
	Bootstrapped bool     `json:"bootstrapped"`
	Workspaces   []Record `json:"workspaces"`
}

// Store is the on-disk workspace registry.
type Store struct {
	mu           sync.Mutex
	home         string
	sessionsRoot string
	data         fileData
}

// Open loads {home}/workspaces.json (missing file is empty).
func Open(home, sessionsRoot string) *Store {
	s := &Store{home: home, sessionsRoot: sessionsRoot, data: fileData{Version: version}}
	b, err := os.ReadFile(s.path())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s.data)
	if s.data.Version == 0 {
		s.data.Version = version
	}
	return s
}

func (s *Store) path() string { return filepath.Join(s.home, "workspaces.json") }

// List returns registry order.
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.data.Workspaces))
	copy(out, s.data.Workspaces)
	return out
}

// Get looks up by id.
func (s *Store) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(id)
	if i < 0 {
		return Record{}, false
	}
	return s.data.Workspaces[i], true
}

// Create registers path (mkdir if missing). A duplicate path returns the existing record.
func (s *Store) Create(path, title string) (Record, bool, error) {
	norm, err := Normalize(path, s.home)
	if err != nil {
		return Record{}, false, err
	}
	st, err := os.Stat(norm)
	if err != nil {
		if !os.IsNotExist(err) {
			return Record{}, false, err
		}
		if err := os.MkdirAll(norm, 0o755); err != nil {
			return Record{}, false, err
		}
	} else if !st.IsDir() {
		return Record{}, false, fmt.Errorf("not a directory: %s", norm)
	}
	if resolved, err := filepath.EvalSymlinks(norm); err == nil {
		norm = resolved
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.data.Workspaces {
		if SamePath(rec.Path, norm) {
			return rec, false, nil
		}
	}
	if strings.TrimSpace(title) == "" {
		title = filepath.Base(norm)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := Record{
		ID:        idgen.NewV7(),
		Path:      norm,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.data.Workspaces = append([]Record{rec}, s.data.Workspaces...)
	if err := s.writeLocked(); err != nil {
		s.data.Workspaces = s.data.Workspaces[1:]
		return Record{}, false, err
	}
	return rec, true, nil
}

// EnsureTemp creates {home}/workspace/tmp+<timestamp> and registers it.
func (s *Store) EnsureTemp() (Record, error) {
	parent := filepath.Join(s.home, "workspace")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Record{}, err
	}
	base := "tmp+" + idgen.FileTimestamp(time.Now())
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = base + "-" + strconv.Itoa(i)
		}
		dir := filepath.Join(parent, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return Record{}, err
		}
		rec, _, err := s.Create(dir, filepath.Base(dir))
		return rec, err
	}
	return Record{}, errors.New("temp workspace")
}

// SetTitle updates the display title.
func (s *Store) SetTitle(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("title required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(id)
	if i < 0 {
		return errNotFound
	}
	s.data.Workspaces[i].Title = title
	s.data.Workspaces[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.writeLocked()
}

// InsertBefore moves id before beforeID (empty beforeID appends).
func (s *Store) InsertBefore(id, beforeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.indexLocked(id)
	if from < 0 {
		return errNotFound
	}
	if beforeID != "" && s.indexLocked(beforeID) < 0 {
		return errNotFound
	}
	rec := s.data.Workspaces[from]
	rest := append(append([]Record{}, s.data.Workspaces[:from]...), s.data.Workspaces[from+1:]...)
	if beforeID == "" {
		s.data.Workspaces = append(rest, rec)
	} else {
		to := -1
		for i, r := range rest {
			if r.ID == beforeID {
				to = i
				break
			}
		}
		if to < 0 {
			return errNotFound
		}
		s.data.Workspaces = append(rest[:to], append([]Record{rec}, rest[to:]...)...)
	}
	return s.writeLocked()
}

// InsertSessionBefore moves sid before beforeID within the workspace account.
func (s *Store) InsertSessionBefore(wsID, sid, beforeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(wsID)
	if i < 0 {
		return errNotFound
	}
	ids := append([]string{}, s.data.Workspaces[i].SessionIDs...)
	from := indexOf(ids, sid)
	if from < 0 {
		return fmt.Errorf("session not in workspace")
	}
	if beforeID != "" && indexOf(ids, beforeID) < 0 {
		return errNotFound
	}
	ids = append(ids[:from], ids[from+1:]...)
	if beforeID == "" {
		ids = append(ids, sid)
	} else {
		to := indexOf(ids, beforeID)
		ids = append(ids[:to], append([]string{sid}, ids[to:]...)...)
	}
	s.data.Workspaces[i].SessionIDs = ids
	s.data.Workspaces[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.writeLocked()
}

// AttachSession prepends sid if not already accounted.
func (s *Store) AttachSession(wsID, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(wsID)
	if i < 0 {
		return errNotFound
	}
	if indexOf(s.data.Workspaces[i].SessionIDs, sid) >= 0 {
		return nil
	}
	s.data.Workspaces[i].SessionIDs = append([]string{sid}, s.data.Workspaces[i].SessionIDs...)
	s.data.Workspaces[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.writeLocked()
}

// DetachSession removes sid from the account.
func (s *Store) DetachSession(wsID, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(wsID)
	if i < 0 {
		return nil
	}
	ids := s.data.Workspaces[i].SessionIDs
	n := indexOf(ids, sid)
	if n < 0 {
		return nil
	}
	s.data.Workspaces[i].SessionIDs = append(ids[:n], ids[n+1:]...)
	s.data.Workspaces[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.writeLocked()
}

// Delete removes the registry row only.
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(id)
	if i < 0 {
		return false, nil
	}
	s.data.Workspaces = append(s.data.Workspaces[:i], s.data.Workspaces[i+1:]...)
	if err := s.writeLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// Match finds the workspace whose path equals cwd.
func (s *Store) Match(cwd string) (Record, bool) {
	norm, err := Normalize(cwd, s.home)
	if err != nil {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.data.Workspaces {
		if SamePath(rec.Path, norm) {
			return rec, true
		}
	}
	return Record{}, false
}

// IsTemp reports whether rec lives under {home}/workspace/tmp+.
func (s *Store) IsTemp(rec Record) bool {
	root := filepath.Join(s.home, "workspace")
	parent := filepath.Dir(rec.Path)
	return SamePath(parent, root) && strings.HasPrefix(filepath.Base(rec.Path), "tmp+")
}

// Bootstrap registers distinct existing cwds once.
func (s *Store) Bootstrap(cwds []string) error {
	s.mu.Lock()
	if s.data.Bootstrapped {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	seen := map[string]bool{}
	for _, cwd := range cwds {
		norm, err := Normalize(cwd, s.home)
		if err != nil {
			continue
		}
		if seen[norm] {
			continue
		}
		st, err := os.Stat(norm)
		if err != nil || !st.IsDir() {
			continue
		}
		seen[norm] = true
		if _, _, err := s.Create(norm, ""); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Bootstrapped = true
	return s.writeLocked()
}

// SafeToRemoveDir rejects volume roots, user home, KI_HOME, and the sessions root.
func SafeToRemoveDir(path, home, sessionsRoot string) error {
	norm, err := Normalize(path, home)
	if err != nil {
		return err
	}
	vol := filepath.VolumeName(norm)
	root := vol + string(os.PathSeparator)
	if norm == "/" || norm == vol || norm == root || norm == vol+"/" {
		return errors.New("refusing to delete volume root")
	}
	userHome, _ := os.UserHomeDir()
	for _, p := range []string{userHome, home, sessionsRoot} {
		if p == "" {
			continue
		}
		n, err := Normalize(p, home)
		if err != nil {
			continue
		}
		if SamePath(norm, n) {
			return fmt.Errorf("refusing to delete %s", n)
		}
	}
	return nil
}

// Normalize Abs+Clean, then EvalSymlinks when the path exists.
func Normalize(path, base string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(path) {
		if base == "" {
			return "", errors.New("relative path")
		}
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	return path, nil
}

// SamePath compares normalized paths; case-insensitive on Windows and Darwin.
func SamePath(a, b string) bool {
	if a == b {
		return true
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return false
}

// ErrNotFound is a missing workspace id.
var ErrNotFound = errors.New("workspace not found")

var errNotFound = ErrNotFound

func (s *Store) indexLocked(id string) int {
	for i, rec := range s.data.Workspaces {
		if rec.ID == id {
			return i
		}
	}
	return -1
}

//write-to-temp-then-rename
func (s *Store) writeLocked() error {
	if err := os.MkdirAll(s.home, 0o755); err != nil {
		return err
	}
	s.data.Version = version
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

func indexOf(ids []string, id string) int {
	for i, v := range ids {
		if v == id {
			return i
		}
	}
	return -1
}

// NotFound reports whether err is a missing workspace.
func NotFound(err error) bool {
	return errors.Is(err, errNotFound)
}
