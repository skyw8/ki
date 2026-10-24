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
	mu       sync.Mutex
	rows     map[string]*cachedInfo
	workers  chan struct{}
	onChange func()
	closed   bool
	stop     chan struct{}
	wg       sync.WaitGroup
}

type cachedInfo struct {
	mu           sync.Mutex
	info         Info
	jsonl        fileStamp
	config       fileStamp
	loaded       bool
	leaf         string
	linksSummary *questionListSummary
	linksLeaf    string
	links        []QuestionLink
	summary      *questionListSummary
	building     bool
}

type fileStamp struct {
	size  int64
	mtime time.Time
}

func NewListCache() *ListCache {
	return &ListCache{rows: map[string]*cachedInfo{}, workers: make(chan struct{}, 2), stop: make(chan struct{})}
}

func (c *ListCache) SetChangeHandler(handler func()) {
	c.mu.Lock()
	c.onChange = handler
	c.mu.Unlock()
}

func (c *ListCache) List(root string) ([]Info, error) {
	seen := map[string]bool{}
	var out []Info
	err := walkSessionDirs(root, func(dir string) bool {
		seen[dir] = true
		if info, err := c.Row(dir); err == nil {
			out = append(out, info)
		}
		return true
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	c.mu.Lock()
	for dir := range c.rows {
		if filepath.Dir(filepath.Dir(dir)) == filepath.Clean(root) && !seen[dir] {
			delete(c.rows, dir)
		}
	}
	c.mu.Unlock()
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(b.Timestamp, a.Timestamp) })
	return out, nil
}

func (c *ListCache) row(dir string) *cachedInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = map[string]*cachedInfo{}
	}
	if c.workers == nil {
		c.workers = make(chan struct{}, 2)
	}
	if c.stop == nil {
		c.stop = make(chan struct{})
	}
	row := c.rows[dir]
	if row == nil {
		row = &cachedInfo{}
		c.rows[dir] = row
	}
	return row
}

func (c *ListCache) Row(dir string) (Info, error) {
	dir = filepath.Clean(dir)
	row := c.row(dir)
	row.mu.Lock()
	defer row.mu.Unlock()
	jsonl, err := stampFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return Info{}, err
	}
	config, err := stampFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return Info{}, err
	}
	if !row.loaded || row.jsonl != jsonl || row.config != config {
		info, err := liteInfo(dir)
		if err != nil {
			return Info{}, err
		}
		cfg, err := ReadConfig(dir)
		if err != nil {
			return Info{}, err
		}
		row.leaf = cfg.ActiveLeafID
		row.info, row.jsonl, row.config, row.loaded = info, jsonl, config, true
	}
	info := row.info
	if info.Metadata["purpose"] == "question_side" {
		return info, nil
	}
	if row.summary == nil {
		row.summary = readQuestionListSummary(dir)
	}
	if row.summary != nil {
		if row.linksSummary != row.summary || row.linksLeaf != row.leaf {
			row.links = row.summary.links(row.leaf)
			row.linksSummary, row.linksLeaf = row.summary, row.leaf
		}
		info.QuestionLinks = row.links
	}
	info.QuestionLinksLoading = row.summary == nil || !row.summary.matches(jsonl)
	if info.QuestionLinksLoading && !row.building {
		row.building = true
		c.mu.Lock()
		if !c.closed {
			c.wg.Add(1)
			go c.buildQuestions(dir, row)
		} else {
			row.building = false
		}
		c.mu.Unlock()
	}
	return info, nil
}

func (c *ListCache) buildQuestions(dir string, row *cachedInfo) {
	defer c.wg.Done()
	select {
	case c.workers <- struct{}{}:
	case <-c.stop:
		return
	}
	defer func() { <-c.workers }()
	row.mu.Lock()
	prior := row.summary
	row.mu.Unlock()
	next, err := buildQuestionListSummary(dir, prior, c.stop)
	row.mu.Lock()
	row.building = false
	if err == nil {
		row.summary = next
	}
	row.mu.Unlock()
	if err == nil {
		c.mu.Lock()
		handler := c.onChange
		c.mu.Unlock()
		if handler != nil {
			handler()
		}
	}
}

func (c *ListCache) Close() {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		if c.stop != nil {
			close(c.stop)
		}
	}
	c.mu.Unlock()
	c.wg.Wait()
}

func stampFile(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{size: info.Size(), mtime: info.ModTime()}, nil
}
