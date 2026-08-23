package tools

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
)

type mutationEntry struct {
	token chan struct{}
	refs  int
}

// MutationQueue serializes mutations of the same canonical host path while
// allowing unrelated files to proceed independently.
type MutationQueue struct {
	mu      sync.Mutex
	entries map[string]*mutationEntry
}

// NewMutationQueue creates an empty path mutation queue.
func NewMutationQueue() *MutationQueue { return &MutationQueue{entries: map[string]*mutationEntry{}} }

func canonicalMutationPath(path string) string {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path)); parentErr == nil {
		path = filepath.Join(parent, filepath.Base(path))
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (q *MutationQueue) acquire(ctx context.Context, key string) (func(), error) {
	q.mu.Lock()
	e := q.entries[key]
	if e == nil {
		e = &mutationEntry{token: make(chan struct{}, 1)}
		e.token <- struct{}{}
		q.entries[key] = e
	}
	e.refs++
	q.mu.Unlock()
	select {
	case <-ctx.Done():
		q.drop(key, e)
		return nil, ctx.Err()
	case <-e.token:
	}
	return func() {
		e.token <- struct{}{}
		q.drop(key, e)
	}, nil
}

func (q *MutationQueue) drop(key string, e *mutationEntry) {
	q.mu.Lock()
	e.refs--
	if e.refs == 0 && q.entries[key] == e {
		delete(q.entries, key)
	}
	q.mu.Unlock()
}

// LockPaths acquires all distinct paths in a stable order and returns a release function.
func (q *MutationQueue) LockPaths(ctx context.Context, paths ...string) (func(), error) {
	keys := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		key := canonicalMutationPath(path)
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	releases := make([]func(), 0, len(keys))
	for _, key := range keys {
		release, err := q.acquire(ctx, key)
		if err != nil {
			for _, release := range slices.Backward(releases) {
				release()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	return func() {
		for _, release := range slices.Backward(releases) {
			release()
		}
	}, nil
}
