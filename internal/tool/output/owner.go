package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	baseDirName   = "ki-tool-output"
	runDirPrefix  = "run-"
	ownerFileName = "owner.json"
)

type ownerInfo struct {
	PID       int       `json:"pid"`
	Host      string    `json:"host"`
	StartedAt time.Time `json:"startedAt"`
	Heartbeat time.Time `json:"heartbeat"`
}

func (s *Store) writeOwner() error {
	info := ownerInfo{PID: os.Getpid(), Host: s.host, StartedAt: s.now(), Heartbeat: s.now()}
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encode tool output owner: %w", err)
	}
	if err := os.WriteFile(s.ownerPath, raw, 0o600); err != nil {
		return fmt.Errorf("write tool output owner: %w", err)
	}
	return nil
}

func (s *Store) touchOwner() {
	if s.ownerPath == "" {
		return
	}
	now := s.now()
	_ = os.Chtimes(s.ownerPath, now, now)
}

func (s *Store) sweep(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	now := s.now()
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), runDirPrefix) {
			continue
		}
		path := filepath.Join(base, entry.Name())
		if s.abandoned(path, now) {
			_ = os.RemoveAll(path)
		}
	}
}

func (s *Store) abandoned(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	heartbeat := info.ModTime()
	owner, err := readOwner(filepath.Join(path, ownerFileName))
	if err != nil || owner == nil {
		return now.Sub(heartbeat) > s.ttl
	}
	if owner.Heartbeat.After(heartbeat) {
		heartbeat = owner.Heartbeat
	}
	if owner.Host != s.host {
		return now.Sub(heartbeat) > s.ttl
	}
	if owner.PID == os.Getpid() {
		return false
	}
	if alive, known := processAlive(owner.PID); known {
		return !alive
	}
	return now.Sub(heartbeat) > s.ttl
}

func readOwner(path string) (*ownerInfo, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is inside this package's base directory
	if err != nil {
		return nil, err
	}
	var info ownerInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	if info.PID <= 0 && info.Host == "" {
		return nil, nil
	}
	return &info, nil
}
