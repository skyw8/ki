package output

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"ki/internal/process"
)

const (
	DefaultMaxProcessFileBytes    int64 = 64 << 20
	DefaultMaxProcessSessionBytes int64 = 512 << 20
	DefaultMaxProcessServiceBytes int64 = 2 << 30
)

var ErrProcessBudget = errors.New("terminal output quota exhausted")

type processFile struct {
	mu      sync.Mutex
	file    *os.File
	store   *Store
	session string
	written int64
	closed  bool
}

func positiveLimit(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func (s *Store) CreateProcessOutputFile(sessionID, toolName string) (process.OutputFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processClosed || s.closedProcessSessions[sessionID] {
		return nil, os.ErrClosed
	}
	f, err := s.CreateOutputFile(sessionID, toolName)
	if err != nil {
		return nil, err
	}
	writer := &processFile{file: f, store: s, session: sessionID}
	if s.processFiles[sessionID] == nil {
		s.processFiles[sessionID] = map[*processFile]struct{}{}
	}
	s.processFiles[sessionID][writer] = struct{}{}
	return writer, nil
}

func (f *processFile) Write(data []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	s := f.store
	s.mu.Lock()
	if s.processClosed || s.closedProcessSessions[f.session] {
		s.mu.Unlock()
		return 0, os.ErrClosed
	}
	available := min(s.processFileLimit-f.written, s.processSessionLimit-s.processBytes[f.session], s.processServiceLimit-s.processTotal)
	keep := min(int64(len(data)), max(available, 0))
	s.processBytes[f.session] += keep
	s.processTotal += keep
	s.mu.Unlock()
	n, err := f.file.Write(data[:int(keep)])
	f.written += int64(n)
	s.mu.Lock()
	if !s.processClosed && !s.closedProcessSessions[f.session] {
		s.processBytes[f.session] -= keep - int64(n)
		s.processTotal -= keep - int64(n)
	}
	s.mu.Unlock()
	if err == nil && n < len(data) {
		err = fmt.Errorf("%w: stored %d bytes; raw recovery is limited to this prefix (file=%d, session=%d, service=%d bytes)", ErrProcessBudget, f.written, s.processFileLimit, s.processSessionLimit, s.processServiceLimit)
	}
	return n, err
}

func (f *processFile) Name() string { return f.file.Name() }
func (f *processFile) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return os.ErrClosed
	}
	return f.file.Sync()
}
func (f *processFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	f.store.mu.Lock()
	delete(f.store.processFiles[f.session], f)
	if len(f.store.processFiles[f.session]) == 0 {
		delete(f.store.processFiles, f.session)
	}
	f.store.mu.Unlock()
	return f.file.Close()
}

func (s *Store) takeProcessFilesLocked(session string) []*processFile {
	var files []*processFile
	for owner, entries := range s.processFiles {
		if session != "" && owner != session {
			continue
		}
		for file := range entries {
			files = append(files, file)
		}
		delete(s.processFiles, owner)
	}
	return files
}
