package server

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ki/internal/session"
)

type fileStamp struct {
	size  int64
	mtime time.Time
}

type sessionSnap struct {
	jsonl   fileStamp
	config  fileStamp
	id      string
	dir     string
	header  session.Header
	configV session.Config
	leafID  string
	entries []session.Entry
}

func stampOf(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{size: info.Size(), mtime: info.ModTime()}, nil
}

func (s *Server) dropSessionSnap(id string) {
	s.snapMu.Lock()
	delete(s.snaps, id)
	s.snapMu.Unlock()
}

func (s *Server) loadSessionSnap(id string) (*sessionSnap, error) {
	dir, err := s.sessionDir(id)
	if err != nil {
		return nil, err
	}
	jsonlStamp, err := stampOf(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("stat events.jsonl: %w", err)
	}
	configStamp, err := stampOf(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("stat config.json: %w", err)
	}
	s.snapMu.Lock()
	cached := s.snaps[id]
	if cached != nil && cached.jsonl.size == jsonlStamp.size && cached.jsonl.mtime.Equal(jsonlStamp.mtime) &&
		cached.config.size == configStamp.size && cached.config.mtime.Equal(configStamp.mtime) {
		s.snapMu.Unlock()
		return cached, nil
	}
	s.snapMu.Unlock()

	sess, err := session.Open(dir)
	if err != nil {
		return nil, err
	}
	snap := &sessionSnap{
		jsonl:   jsonlStamp,
		config:  configStamp,
		id:      sess.ID(),
		dir:     sess.Dir,
		header:  sess.Header,
		configV: sess.Config,
		leafID:  sess.LeafID(),
		entries: sess.Entries(),
	}
	_ = sess.Close()

	s.snapMu.Lock()
	s.snaps[id] = snap
	s.snapMu.Unlock()
	return snap, nil
}

func (s *Server) sessionDir(id string) (string, error) {
	if dir, ok := s.sidx.Lookup(id); ok {
		if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err == nil {
			return dir, nil
		}
		s.sidx.Remove(id)
	}
	dir, err := session.Find(s.cfg.Sessions.Root, id)
	if err != nil {
		return "", fmt.Errorf("find session: %w", err)
	}
	s.sidx.Add(id, dir)
	return dir, nil
}
