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
)

// Why: events.jsonl is append-only, so a reader can keep the entries it already
// decoded and extend them by decoding only the bytes appended since. The WebUI
// opens a session on every switch and reopens it after every run, and a cold
// parse of a 40 MB transcript costs about a second of JSON work; with the cache
// below that cost is paid once per process, and every later read is
// proportional to what was appended rather than to the size of the history.
//
// The filesystem stays the source of truth: every read revalidates the file by
// size+mtime, a file that only grew is extended from the recorded offset, and a
// file that shrank or was rewritten (moved clock, replaced directory) is read
// from scratch.
//
// Callers must treat the returned slice as read-only: it is shared with the
// cache and with every other caller until the file changes.

// tailReadBytes is the first backward read of a tail request. One megabyte
// covers the default 100-entry window for ordinary transcripts; a tool-heavy
// transcript grows from there.
const tailReadBytes = 1 << 20

// TailReadLimit is the transcript size below which one tail read covers the
// whole file. Callers use it to tell "a small session that was read in full"
// from "a long session that happens to be cached in full", which matters when
// deciding whether extra derived data is free to include.
const TailReadLimit = tailReadBytes

// tailGrowBytes is the step a short window grows by. It is smaller than the
// first read because overshooting costs a full parse of the extra bytes for
// entries the caller never sees: a window that missed its target by a few
// entries should not pull in another megabyte.
const tailGrowBytes = 256 << 10

// entriesCache holds the entries decoded from one events.jsonl so far.
type entriesCache struct {
	mu      sync.Mutex
	start   int64 // byte offset of the first cached entry's line; 0 means the window reaches the file start
	size    int64 // byte offset just past the last cached entry's line
	mtime   time.Time
	entries []Entry
}

var entriesCaches sync.Map // cleaned dir → *entriesCache

func entriesCacheFor(dir string) *entriesCache {
	key := filepath.Clean(dir)
	v, _ := entriesCaches.LoadOrStore(key, &entriesCache{})
	c, ok := v.(*entriesCache)
	if !ok {
		c = &entriesCache{}
		entriesCaches.Store(key, c)
	}
	return c
}

// DropEntriesCache forgets what was decoded for a session directory. The server
// calls it when a session is deleted so the cache cannot pin a removed
// transcript in memory; a stale entry is harmless anyway, because every read
// revalidates by size+mtime.
func DropEntriesCache(dir string) {
	entriesCaches.Delete(filepath.Clean(dir))
}

func (c *entriesCache) reset() {
	c.start, c.size, c.mtime, c.entries = 0, 0, time.Time{}, nil
}

// entriesPath is the transcript path of a session directory.
func entriesPath(dir string) string { return filepath.Join(dir, "events.jsonl") }

// ReadHeader reads the session header (the first jsonl line).
func ReadHeader(dir string) (Header, error) {
	//nolint:gosec // dir is an internally generated session directory.
	f, err := os.Open(entriesPath(dir))
	if err != nil {
		return Header{}, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
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

// ReadConfig reads a session's config.json. It takes the same file gate as Open
// and writeConfig: on Windows the reader that lands inside a replace fails with
// ERROR_SHARING_VIOLATION ("being used by another process"), so config reads and
// writes have to be ordered rather than raced.
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

// AllEntries returns every entry of the session transcript.
func AllEntries(dir string) ([]Entry, error) {
	c := entriesCacheFor(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(dir, 0, true); err != nil {
		return nil, err
	}
	return c.entries, nil
}

// TailEntries returns the trailing window of the transcript that holds at least
// want entries (fewer only when the session has fewer). complete reports whether
// the window reaches the file's first entry, i.e. whether it is the whole
// history.
func TailEntries(dir string, want int) ([]Entry, bool, error) {
	c := entriesCacheFor(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(dir, want, false); err != nil {
		return nil, false, err
	}
	return c.entries, c.start == 0, nil
}

// LeafTail returns the trailing window covering the last want entries of the
// active leaf chain. It grows the window when the tail holds fewer chain entries
// (a branchy or tool-heavy tail carries many entries off the branch), so callers
// can treat the result as "the newest want messages of this branch".
func LeafTail(dir, leafID string, want int) ([]Entry, bool, error) {
	entries, complete, err := TailEntries(dir, want)
	if err != nil {
		return nil, false, err
	}
	// The window grows until the leaf chain is covered; TailEntries reports
	// complete once the window reaches the file start, which ends the loop even
	// when the chain is shorter than want.
	for !complete && len(LeafChain(entries, leafID)) < want {
		// Grow by a fraction rather than a multiple: the window only has to
		// cover the chain, and every extra entry is parsed and trimmed anyway.
		want += want/4 + 8
		entries, complete, err = TailEntries(dir, want)
		if err != nil {
			return nil, false, err
		}
	}
	return entries, complete, nil
}

// load brings the cache up to the requested window. want is the minimum number
// of trailing entries (0 for none); full requires a window that starts at the
// first entry.
func (c *entriesCache) load(dir string, want int, full bool) error {
	path := entriesPath(dir)
	stamp, err := stampFile(path)
	if err != nil {
		c.reset()
		return err
	}
	switch {
	case c.size == 0 && c.mtime.IsZero():
		// Cold cache: nothing decoded yet.
	case stamp.size == c.size && stamp.mtime.Equal(c.mtime):
		// Unchanged since the last read: the window only has to be big enough.
		if (full && c.start == 0) || (!full && (want <= 0 || len(c.entries) >= want || c.start == 0)) {
			return nil
		}
	case stamp.size > c.size && !stamp.mtime.Before(c.mtime):
		added, err := readEntries(path, c.size, stamp.size)
		if err != nil {
			c.reset()
			return err
		}
		c.entries = append(c.entries, added.entries...)
		c.size = added.end
		c.mtime = stamp.mtime
	default:
		// Truncated, replaced, or rewritten: the decoded window no longer
		// describes the file.
		c.reset()
	}

	if c.size == 0 {
		if full {
			return c.readAll(path, stamp)
		}
		// Start from a bounded tail of the file and grow downward below.
		start := max(stamp.size-tailReadBytes, 0)
		tail, err := readEntries(path, start, stamp.size)
		if err != nil {
			return err
		}
		c.start, c.size, c.mtime, c.entries = tail.start, tail.end, stamp.mtime, tail.entries
	} else if full && c.start > 0 {
		prefix, err := readEntries(path, 0, c.start)
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
		before := len(c.entries)
		head, err := readEntries(path, start, c.start)
		if err != nil {
			return err
		}
		if head.end != c.start || len(c.entries) == before {
			// The range before the window held no complete line: fall back to a
			// full read so callers always get an answer instead of looping.
			return c.readAll(path, stamp)
		}
		merged := make([]Entry, 0, len(head.entries)+len(c.entries))
		merged = append(merged, head.entries...)
		merged = append(merged, c.entries...)
		c.entries = merged
		c.start = head.start
	}
	return nil
}

// readAll decodes the whole file into the cache.
func (c *entriesCache) readAll(path string, stamp fileStamp) error {
	all, err := readEntries(path, 0, stamp.size)
	if err != nil {
		return err
	}
	c.start, c.size, c.mtime, c.entries = 0, all.end, stamp.mtime, all.entries
	return nil
}

// OpenFrom builds a Session handle over an already-read header, config and
// entry list, so a caller that just took them from the shared entry cache does
// not decode the transcript a second time. The entries are copied: the cache
// shares its slice with other readers, and the returned Session appends to its
// own copy (with cap == len, so an append can never write into the shared
// backing array).
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
		// Config may point at a leaf that is not in the read window (a
		// concurrent append): keep the newest entry that was read instead.
	}
	return s
}

// entryRange is the decoded result of one contiguous byte range.
type entryRange struct {
	entries []Entry
	start   int64 // offset of the first decoded line (or of the resume point when the range decoded nothing)
	end     int64 // offset just past the last decoded line
}

// readEntries decodes the whole jsonl lines in [from, to). A range that starts
// mid-line (a backward read) drops that partial line, and a range that ends in a
// half-written line stops before it, so the next read picks that line up again;
// start/end always sit on line boundaries.
func readEntries(path string, from, to int64) (entryRange, error) {
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
	headerPending := from == 0 // the file's first line is the session header, not an entry
	started := false
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break // trailing line without a newline: leave it for the next read
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
		var e Entry
		if err := json.Unmarshal(trimmed, &e); err != nil {
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
