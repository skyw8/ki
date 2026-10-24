package session

import (
	"os"
	"path/filepath"
	"sync"
)

type Index struct {
	mu   sync.RWMutex
	byID map[string]string
}

func NewIndex(infos []Info) *Index {
	ix := &Index{byID: make(map[string]string, len(infos))}
	for _, in := range infos {
		ix.byID[in.ID] = in.Dir
	}
	return ix
}

func (ix *Index) Lookup(id string) (string, bool) {
	ix.mu.RLock()
	dir, ok := ix.byID[id]
	ix.mu.RUnlock()
	return dir, ok
}

func (ix *Index) Add(id, dir string) {
	ix.mu.Lock()
	ix.byID[id] = dir
	ix.mu.Unlock()
}

func (ix *Index) Remove(id string) {
	ix.mu.Lock()
	delete(ix.byID, id)
	ix.mu.Unlock()
}

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
