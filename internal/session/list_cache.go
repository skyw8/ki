package session

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

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

func NewListCache() *ListCache {
	return &ListCache{rows: map[string]cachedInfo{}}
}

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
	for dir := range c.rows {
		if _, ok := seen[dir]; !ok {
			delete(c.rows, dir)
		}
	}
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(b.Timestamp, a.Timestamp) })
	return out, nil
}

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
