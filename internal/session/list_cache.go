package session

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// ListCache reuses list rows across calls.
//
// Why: the WebUI fetches GET /v1/sessions on every refresh. Even with the lite
// scan, re-reading config.json plus the first jsonl lines for every session on
// every request is avoidable work once nothing changed. A row is reused only
// while events.jsonl and config.json keep the same size and mtime, so the
// filesystem stays the source of truth: a change written by another process
// still surfaces on the next call.
//
// Rows are treated as immutable. Callers must not mutate the returned Info or
// its Metadata map; they may be shared with earlier results.
type ListCache struct {
	mu   sync.Mutex
	rows map[string]cachedInfo
}

type cachedInfo struct {
	info   Info
	jsonl  fileStamp
	config fileStamp
}

type fileStamp struct {
	size  int64
	mtime time.Time
}

// NewListCache returns an empty cache.
func NewListCache() *ListCache {
	return &ListCache{rows: map[string]cachedInfo{}}
}

// List returns the same rows as List, reusing rows whose files are unchanged.
//
// The whole walk runs under the cache lock, so a burst of concurrent requests
// does one pass of revalidation instead of one per request.
func (c *ListCache) List(root string) ([]Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = map[string]cachedInfo{}
	}
	seen := make(map[string]struct{}, len(c.rows))
	var out []Info
	err := walkSessionDirs(root, func(dir string) bool {
		seen[dir] = struct{}{}
		jsonl, err := stampFile(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			delete(c.rows, dir)
			return true
		}
		config, err := stampFile(filepath.Join(dir, "config.json"))
		if err != nil {
			delete(c.rows, dir)
			return true
		}
		if row, ok := c.rows[dir]; ok && row.jsonl == jsonl && row.config == config {
			out = append(out, row.info)
			return true
		}
		info, err := liteInfo(dir)
		if err != nil {
			delete(c.rows, dir)
			return true
		}
		c.rows[dir] = cachedInfo{info: info, jsonl: jsonl, config: config}
		out = append(out, info)
		return true
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Drop rows for directories that no longer exist so a deleted session does
	// not pin its directory in memory forever.
	for dir := range c.rows {
		if _, ok := seen[dir]; !ok {
			delete(c.rows, dir)
		}
	}
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(b.Timestamp, a.Timestamp) })
	return out, nil
}

// Row returns one session's list row, reusing the cached row while the session
// files are unchanged. It is the per-directory counterpart of List: callers
// that hold a directory (the session view needs a title even when it only read
// a tail of the transcript) get the lite row without walking the whole root.
func (c *ListCache) Row(dir string) (Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = map[string]cachedInfo{}
	}
	jsonl, err := stampFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		delete(c.rows, dir)
		return Info{}, err
	}
	config, err := stampFile(filepath.Join(dir, "config.json"))
	if err != nil {
		delete(c.rows, dir)
		return Info{}, err
	}
	if row, ok := c.rows[dir]; ok && row.jsonl == jsonl && row.config == config {
		return row.info, nil
	}
	info, err := liteInfo(dir)
	if err != nil {
		delete(c.rows, dir)
		return Info{}, err
	}
	c.rows[dir] = cachedInfo{info: info, jsonl: jsonl, config: config}
	return info, nil
}

func stampFile(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{size: info.Size(), mtime: info.ModTime()}, nil
}
