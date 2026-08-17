package session

import (
	"os"
	"path/filepath"
	"sync"
)

// Index is an in-memory id → dir map for O(1) session lookup.
//
// It is an optimization over scanning the two-level directory layout on every
// open. The filesystem remains the source of truth: Lookup may miss (session
// created by another process, or stale entry) and callers fall back to Find,
// then re-Add. Entries are only ever keyed by uuidv7 ids, so a stale entry
// can never point at the wrong session.
type Index struct {
	mu   sync.RWMutex
	byID map[string]string
}

// NewIndex builds an index from already-scanned session info (one walk, zero
// extra reads).
func NewIndex(infos []Info) *Index {
	ix := &Index{byID: make(map[string]string, len(infos))}
	for _, in := range infos {
		ix.byID[in.ID] = in.Dir
	}
	return ix
}

// Lookup returns the directory for id.
func (ix *Index) Lookup(id string) (string, bool) {
	ix.mu.RLock()
	dir, ok := ix.byID[id]
	ix.mu.RUnlock()
	return dir, ok
}

// Add records id → dir. Call after session create or fork.
func (ix *Index) Add(id, dir string) {
	ix.mu.Lock()
	ix.byID[id] = dir
	ix.mu.Unlock()
}

// Remove forgets id. Call after session delete, or when an entry is found to
// be stale (dir no longer openable).
func (ix *Index) Remove(id string) {
	ix.mu.Lock()
	delete(ix.byID, id)
	ix.mu.Unlock()
}

// walkSessionDirs visits every session directory under root (two levels: cwd
// dir, then session dir). fn returning false stops the walk early. Errors on
// a single cwd dir are skipped so one broken subtree does not kill the walk;
// root-level errors (including IsNotExist) are returned as-is.
func walkSessionDirs(root string, fn func(dir string) bool) error {
	encs, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, enc := range encs {
		if !enc.IsDir() {
			continue
		}
		inner := filepath.Join(root, enc.Name())
		subs, err := os.ReadDir(inner)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if !sub.IsDir() {
				continue
			}
			if !fn(filepath.Join(inner, sub.Name())) {
				return nil
			}
		}
	}
	return nil
}
