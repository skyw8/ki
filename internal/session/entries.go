package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ki/internal/memory"
)

const tailReadBytes = 1 << 20

const TailReadLimit = tailReadBytes

const tailGrowBytes = 256 << 10

type entriesCache struct {
	mu      sync.Mutex
	start   int64
	size    int64
	mtime   time.Time
	file    os.FileInfo
	entries []Entry
	weight  int64
	prompts promptPool
}

var entriesCaches = newWeightedCache[*entriesCache](EntriesCacheBytes, maxSessionEntriesBytes)

func entriesCacheFor(dir string) *entriesCache {
	return entriesCaches.get(filepath.Clean(dir), func() *entriesCache { return &entriesCache{} })
}

func DropEntriesCache(dir string) {
	entriesCaches.drop(filepath.Clean(dir))
	transcriptCaches.drop(filepath.Clean(dir))
}

func (c *entriesCache) reset() {
	c.start, c.size, c.mtime, c.entries = 0, 0, time.Time{}, nil
	c.weight = 0
	c.prompts = promptPool{}
	c.file = nil
}

func entriesPath(dir string) string { return filepath.Join(dir, "events.jsonl") }

func ReadHeader(dir string) (Header, error) {
	//nolint:gosec // dir is an internally generated session directory.
	f, err := os.Open(entriesPath(dir))
	if err != nil {
		return Header{}, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLLineBytes)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return Header{}, err
		}
		return Header{}, fmt.Errorf("%w: %s", errSessionHeader, dir)
	}
	var h Header
	if err := json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &h); err != nil {
		return Header{}, fmt.Errorf("decode events.jsonl header: %w", err)
	}
	if h.ID == "" {
		return Header{}, fmt.Errorf("%w: %s", errSessionHeader, dir)
	}
	return h, nil
}

func ReadConfig(dir string) (Config, error) {
	gate := fileGate(dir)
	gate.RLock()
	defer gate.RUnlock()
	//nolint:gosec // dir is an internally generated session directory.
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config.json: %w", err)
	}
	return cfg, nil
}

func AllEntries(dir string) ([]Entry, error) {
	c := entriesCacheFor(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { entriesCaches.update(filepath.Clean(dir), c, c.weight) }()
	if err := c.load(dir, 0, true); err != nil {
		return nil, err
	}
	return c.entries, nil
}

func TailEntries(dir string, want int) ([]Entry, bool, error) {
	c := entriesCacheFor(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { entriesCaches.update(filepath.Clean(dir), c, c.weight) }()
	if err := c.load(dir, want, false); err != nil {
		return nil, false, err
	}
	return c.entries, c.start == 0, nil
}

func LeafTail(dir, leafID string, want int) ([]Entry, bool, error) {
	want = max(1, want)
	entries, complete, err := TailEntries(dir, want)
	if err != nil {
		return nil, false, err
	}
	for !complete {
		chain := LeafChain(entries, leafID)
		if len(chain) >= want || (len(chain) > 0 && chain[0].ParentID == "") {
			break
		}
		entries, complete, err = TailEntries(dir, len(entries)+max(8, want/4))
		if err != nil {
			return nil, false, err
		}
	}
	return entries, complete, nil
}

func (c *entriesCache) load(dir string, want int, full bool) (loadErr error) {
	oldSize, oldStart, oldLen := c.size, c.start, len(c.entries)
	defer func() {
		if loadErr != nil {
			c.reset()
			return
		}
		if oldSize != c.size || oldStart != c.start || oldLen != len(c.entries) || (c.weight == 0 && len(c.entries) > 0) {
			c.weight = memory.Weight(struct {
				Entries []Entry
				Systems map[[32]byte]string
				Tools   map[[32]byte][]ToolSchema
			}{c.entries, c.prompts.systems, c.prompts.tools})
		}
	}()
	if !full && c.size-c.start > tailReadBytes+tailGrowBytes && len(c.entries) > max(want*2, DefaultViewLimit*2) {
		c.reset()
	}

	path := entriesPath(dir)
	f, err := os.Open(path)
	if err != nil {
		c.reset()
		return err
	}
	info, err := f.Stat()
	_ = f.Close()
	if err != nil {
		c.reset()
		return err
	}
	stamp := fileStamp{size: info.Size(), mtime: info.ModTime()}
	if c.file != nil && !os.SameFile(c.file, info) {
		c.reset()
	}
	defer func() {
		if loadErr == nil {
			c.file = info
		}
	}()
	switch {
	case c.size == 0 && c.mtime.IsZero():
	case stamp.size == c.size && stamp.mtime.Equal(c.mtime):
		if (full && c.start == 0) || (!full && (want <= 0 || len(c.entries) >= want || c.start == 0)) {
			return nil
		}
	case stamp.size > c.size && !stamp.mtime.Before(c.mtime):
		added, err := readEntriesWithPool(path, c.size, stamp.size, &c.prompts)
		if err != nil {
			c.reset()
			return err
		}
		c.entries = append(c.entries, added.entries...)
		c.size = added.end
		c.mtime = stamp.mtime
	default:
		c.reset()
	}

	if c.size == 0 {
		if full {
			return c.readAll(path, stamp)
		}
		start := max(stamp.size-tailReadBytes, 0)
		tail, err := readEntriesWithPool(path, start, stamp.size, &c.prompts)
		if err != nil {
			return err
		}
		c.start, c.size, c.mtime, c.entries = tail.start, tail.end, stamp.mtime, tail.entries
		if start == 0 {
			c.start = 0
		}
	} else if full && c.start > 0 {
		prefix, err := readEntriesWithPool(path, 0, c.start, &c.prompts)
		if err != nil {
			return err
		}
		if prefix.end != c.start {
			return c.readAll(path, stamp)
		}
		merged := make([]Entry, 0, len(prefix.entries)+len(c.entries))
		merged = append(merged, prefix.entries...)
		merged = append(merged, c.entries...)
		c.entries = merged
		c.start = 0
	}
	if full || c.start == 0 {
		return nil
	}
	for want > 0 && len(c.entries) < want && c.start > 0 {
		start := max(c.start-tailGrowBytes, 0)
		head, err := readEntriesWithPool(path, start, c.start, &c.prompts)
		if err != nil {
			return err
		}
		if head.end != c.start || len(head.entries) == 0 {
			return c.readAll(path, stamp)
		}
		merged := make([]Entry, 0, len(head.entries)+len(c.entries))
		merged = append(merged, head.entries...)
		merged = append(merged, c.entries...)
		c.entries = merged
		c.start = head.start
		if start == 0 {
			c.start = 0
		}
	}
	return nil
}

func (c *entriesCache) readAll(path string, stamp fileStamp) error {
	all, err := readEntriesWithPool(path, 0, stamp.size, &c.prompts)
	if err != nil {
		return err
	}
	c.start, c.size, c.mtime, c.entries = 0, all.end, stamp.mtime, all.entries
	return nil
}

func OpenFrom(dir string, header Header, cfg Config, entries []Entry) *Session {
	s := &Session{Dir: dir, Header: header, Config: cfg, byID: make(map[string]Entry, len(entries))}
	if len(entries) > 0 {
		s.entries = make([]Entry, len(entries))
		copy(s.entries, entries)
	}
	for _, e := range s.entries {
		s.byID[e.ID] = e
		if !e.Sideband {
			s.leafID = e.ID
		}
	}
	if cfg.ActiveLeafID != "" {
		if _, ok := s.byID[cfg.ActiveLeafID]; ok {
			s.leafID = cfg.ActiveLeafID
		}
	}
	return s
}

type entryRange struct {
	entries []Entry
	start   int64
	end     int64
}

func readEntries(path string, from, to int64) (entryRange, error) {
	var pool promptPool
	return readEntriesWithPool(path, from, to, &pool)
}

func readEntriesWithPool(path string, from, to int64, pool *promptPool) (entryRange, error) {
	out := entryRange{start: from, end: from}
	if to <= from {
		return out, nil
	}
	//nolint:gosec // path is an internally generated session file.
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer func() { _ = f.Close() }()
	skipPartial := false
	if from > 0 {
		var prev [1]byte
		if _, err := f.ReadAt(prev[:], from-1); err != nil {
			return out, err
		}
		skipPartial = prev[0] != '\n'
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return out, err
	}
	r := bufio.NewReaderSize(io.LimitReader(f, to-from), 256*1024)
	offset := from
	headerPending := from == 0
	started := false
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		lineStart := offset
		offset += int64(len(line))
		if skipPartial {
			skipPartial = false
			out.start, out.end = offset, offset
			continue
		}
		trimmed := bytes.TrimSpace(line)
		if headerPending {
			if len(trimmed) == 0 {
				out.start, out.end = offset, offset
				continue
			}
			headerPending = false
			var h Header
			if err := json.Unmarshal(trimmed, &h); err != nil {
				return out, fmt.Errorf("decode events.jsonl header: %w", err)
			}
			if h.ID == "" {
				return out, fmt.Errorf("%w: %s", errSessionHeader, path)
			}
			out.start, out.end = offset, offset
			continue
		}
		if len(trimmed) == 0 {
			if !started {
				out.start, out.end = offset, offset
			}
			continue
		}
		e, err := decodeEntry(trimmed, pool)
		if err != nil {
			return out, fmt.Errorf("decode events.jsonl entry: %w", err)
		}
		if !started {
			started = true
			out.start = lineStart
		}
		out.entries = append(out.entries, e)
		out.end = offset
	}
	if !started {
		out.start, out.end = offset, offset
	}
	return out, nil
}
