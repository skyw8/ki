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
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"ki/internal/memory"
	"ki/internal/types"
)

// Transcript is an immutable body-free snapshot of an events.jsonl file.
// Offsets hydrate only selected entries; historical prompts, arguments,
// attachments, progress payloads and provider checkpoints are not retained.
type Transcript struct {
	dir       string
	stamp     fileStamp
	file      os.FileInfo
	end       int64
	entries   []Entry
	index     []IndexEntry
	locations map[string]entryLocation
}

type entryLocation struct{ offset, bytes int64 }
type transcriptCache struct {
	mu       sync.Mutex
	snapshot *Transcript
	weight   int64
}

var transcriptCaches = newWeightedCache[*transcriptCache](TranscriptCacheBytes, maxSessionTranscriptBytes)

// ReadTranscript incrementally scans metadata, revalidating the filesystem on
// every call. Old snapshots remain valid across appends and cache eviction.
func ReadTranscript(dir string) (*Transcript, error) {
	key := filepath.Clean(dir)
	c := transcriptCaches.get(key, func() *transcriptCache { return &transcriptCache{} })
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() { transcriptCaches.update(key, c, c.weight) }()
	gate := fileGate(dir)
	gate.RLock()
	defer gate.RUnlock()
	f, err := os.Open(entriesPath(dir))
	if err != nil {
		c.snapshot, c.weight = nil, 0
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stamp := fileStamp{size: info.Size(), mtime: info.ModTime()}
	old := c.snapshot
	if old != nil && os.SameFile(old.file, info) && old.stamp == stamp {
		return old, nil
	}
	next := &Transcript{dir: dir, stamp: stamp, file: info, locations: map[string]entryLocation{}}
	if old != nil && os.SameFile(old.file, info) && stamp.size > old.stamp.size && !stamp.mtime.Before(old.stamp.mtime) {
		next.end = old.end
		next.entries = slices.Clone(old.entries)
		next.index = slices.Clone(old.index)
		for id, loc := range old.locations {
			next.locations[id] = loc
		}
	}
	if _, err := f.Seek(next.end, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(io.LimitReader(f, stamp.size-next.end), 256<<10)
	estimates := contextEstimateCache{}
	for {
		line, err := readTranscriptLine(r)
		if errors.Is(err, io.EOF) {
			break
		} // keep a torn append's offset for the next scan
		if err != nil {
			return nil, err
		}
		offset := next.end
		next.end += int64(len(line))
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(next.entries) == 0 && len(next.index) == 0 && next.locations[""].bytes == 0 {
			var h Header
			if err := json.Unmarshal(line, &h); err != nil {
				return nil, err
			}
			if h.ID == "" || h.Type != "session" {
				return nil, fmt.Errorf("%w: %s", errSessionHeader, dir)
			}
			// The header marker also handles a header-only snapshot that grows.
			next.locations[""] = entryLocation{offset, int64(len(line))}
			continue
		}
		e, ix, err := decodeMetadataCached(line, estimates)
		if err != nil {
			return nil, fmt.Errorf("decode transcript metadata: %w", err)
		}
		next.entries = append(next.entries, e)
		next.index = append(next.index, ix)
		next.locations[e.ID] = entryLocation{offset, int64(len(line))}
	}
	c.snapshot = next
	c.weight = memory.Weight(next.entries) + memory.Weight(next.index) + memory.Weight(next.locations) + int64(len(dir)) + 256
	return next, nil
}

// readTranscriptLine bounds transient storage even for a corrupt or enormous
// JSONL record. ReadSlice lets metadata scans reuse the buffer for normal rows.
func readTranscriptLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, err
	}
	out := append([]byte(nil), line...)
	for err == bufio.ErrBufferFull {
		line, err = r.ReadSlice('\n')
		if len(out)+len(line) > maxJSONLLineBytes {
			return nil, fmt.Errorf("events.jsonl record exceeds %d bytes", maxJSONLLineBytes)
		}
		out = append(out, line...)
	}
	return out, err
}

type metadataContent struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Text         string          `json:"text"`
	Thinking     string          `json:"thinking"`
	Input        string          `json:"input"`
	Arguments    json.RawMessage `json:"arguments"`
	ArgumentsRaw string          `json:"argumentsRaw"`
}

type metadataMessage struct {
	Role       string            `json:"role"`
	Origin     string            `json:"origin"`
	ToolCallID string            `json:"toolCallId"`
	ToolName   string            `json:"toolName"`
	StopReason string            `json:"stopReason"`
	IsError    bool              `json:"isError"`
	Timestamp  int64             `json:"timestamp"`
	DurationMs int64             `json:"durationMs"`
	LatencyMs  int64             `json:"latencyMs"`
	TTFTMs     int64             `json:"ttftMs"`
	Usage      *types.Usage      `json:"usage"`
	Content    []metadataContent `json:"content"`
}

func decodeMetadata(raw []byte) (Entry, IndexEntry, error) {
	return decodeMetadataCached(raw, contextEstimateCache{})
}

func decodeMetadataCached(raw []byte, estimates contextEstimateCache) (Entry, IndexEntry, error) {
	var wire struct {
		Type          string           `json:"type"`
		ID            string           `json:"id"`
		ParentID      string           `json:"parentId"`
		Timestamp     string           `json:"timestamp"`
		Sideband      bool             `json:"sideband"`
		TokensBefore  int              `json:"tokensBefore"`
		UsedTokens    int              `json:"usedTokens"`
		ContextWindow int              `json:"contextWindow"`
		Estimated     bool             `json:"estimated"`
		Usage         *types.Usage     `json:"usage"`
		Message       *metadataMessage `json:"message"`
		System        string           `json:"system"`
		Tools         json.RawMessage  `json:"tools"`
		Summary       string           `json:"summary"`
		Responses     *struct{}        `json:"responses"`
		Details       json.RawMessage  `json:"details"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Entry{}, IndexEntry{}, err
	}
	e := Entry{Type: wire.Type, ID: wire.ID, ParentID: wire.ParentID, Timestamp: wire.Timestamp, Sideband: wire.Sideband, TokensBefore: wire.TokensBefore, Usage: wire.Usage, UsedTokens: wire.UsedTokens, ContextWindow: wire.ContextWindow, Estimated: wire.Estimated, System: wire.System, Summary: wire.Summary}
	if wire.Responses != nil {
		e.Responses = &types.ResponsesContext{}
	}
	if w := wire.Message; w != nil {
		m := &types.Message{Role: w.Role, Origin: w.Origin, ToolCallID: w.ToolCallID, ToolName: w.ToolName, StopReason: w.StopReason, IsError: w.IsError, Timestamp: w.Timestamp, DurationMs: w.DurationMs, LatencyMs: w.LatencyMs, TTFTMs: w.TTFTMs, Usage: w.Usage}
		for _, c := range w.Content {
			m.Content = append(m.Content, types.Content{Type: c.Type, ID: c.ID, Name: c.Name, Text: c.Text, Thinking: c.Thinking})
		}
		e.Message = m
		tokens := 0
		for _, c := range w.Content {
			switch c.Type {
			case "text":
				tokens += contextTokens(c.Text)
			case "thinking":
				tokens += contextTokens(c.Thinking)
			case "toolCall":
				arguments := c.ArgumentsRaw
				if len(c.Arguments) > 0 && !bytes.Equal(c.Arguments, []byte("null")) {
					arguments = string(c.Arguments)
				}
				tokens += toolCallTokens(c.Name, c.Input, arguments, nil)
			}
		}
		e.ContextEstimate = &ContextEstimate{Message: contextNumber(tokens)}
	}
	if e.Type == "request_header" {
		tools, err := estimates.tools(wire.Tools)
		if err != nil {
			return Entry{}, IndexEntry{}, err
		}
		e.ContextEstimate = &ContextEstimate{System: contextNumber(contextTokens(e.System)), Tools: contextNumber(tools)}
	}
	e = withContextEstimate(e)
	ix := indexOf(e)
	ix.Preview = strings.Clone(ix.Preview)
	// Why clone bounded previews: a substring would otherwise pin the entire
	// decoded message, defeating the body-free index's memory contract.
	e.System = ""
	e.Summary = metadataPreview(e.Summary)
	if m := e.Message; m != nil {
		text := metadataPreview(m.Text())
		var content []types.Content
		if text != "" {
			content = append(content, types.Content{Type: "text", Text: text})
		}
		for _, c := range m.Content {
			if c.Type == "toolCall" {
				content = append(content, types.Content{Type: c.Type, ID: c.ID, Name: c.Name})
			}
		}
		m.Content = content
	}
	if e.Type == "compaction_end" && len(wire.Details) > 0 {
		var d struct {
			EntryID string `json:"entryId"`
		}
		if err := json.Unmarshal(wire.Details, &d); err != nil {
			return Entry{}, IndexEntry{}, err
		}
		e.Details = map[string]any{"entryId": d.EntryID}
	}
	return e, ix, nil
}

func metadataPreview(s string) string {
	// Keep a few bytes beyond the compact fold's 160-byte cut, so a second
	// whitespace normalization cannot change that boundary. Stop scanning once
	// it is covered instead of allocating fields for an entire hidden body.
	const limit = 164
	var out strings.Builder
	out.Grow(limit + utf8.UTFMax)
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = out.Len() > 0
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
		if out.Len() >= limit {
			break
		}
	}
	return strings.Clone(utf8Prefix(out.String(), limit))
}

// Entries returns read-only structural metadata for branch/turn selection.
// It must never be serialized as if it were a complete entry body.
func (t *Transcript) Entries() []Entry { return t.entries }

// Index returns the ordinary body-less wire index in file order.
func (t *Transcript) Index() []IndexEntry { return t.index }

func (t *Transcript) read(selected []Entry) ([]Entry, error) {
	gate := fileGate(t.dir)
	gate.RLock()
	defer gate.RUnlock()
	f, err := os.Open(entriesPath(t.dir))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(t.file, info) || info.Size() < t.stamp.size || (info.Size() == t.stamp.size && !info.ModTime().Equal(t.stamp.mtime)) || info.ModTime().Before(t.stamp.mtime) {
		return nil, errSessionChanged
	}
	out := make([]Entry, 0, len(selected))
	var pool promptPool
	for _, meta := range selected {
		loc, ok := t.locations[meta.ID]
		if !ok || loc.bytes > maxJSONLLineBytes {
			return nil, errEntryNotFound
		}
		raw := make([]byte, loc.bytes)
		if _, err := f.ReadAt(raw, loc.offset); err != nil {
			return nil, err
		}
		e, err := decodeEntry(raw, &pool)
		if err != nil {
			return nil, err
		}
		if e.ID != meta.ID {
			return nil, errSessionChanged
		}
		// Exact hydration must retain the checkpoint kind without exposing its
		// opaque payload, just like slim views and the body-free index.
		e.ContextEstimate = meta.ContextEstimate
		e = withContextEstimate(e)
		e.RemoteContext = e.RemoteContext || e.Responses != nil
		e.Responses = nil
		out = append(out, e)
	}
	return out, nil
}

// Lookup reads exact bodies by file offset, preserving order and duplicates.
func (t *Transcript) Lookup(ids []string) ([]Entry, error) {
	if len(ids) > MaxViewBatch {
		ids = ids[:MaxViewBatch]
	}
	byID := make(map[string]Entry, len(t.entries))
	for _, e := range t.entries {
		byID[e.ID] = e
	}
	var selected []Entry
	for _, id := range ids {
		if e, ok := byID[strings.TrimSpace(id)]; ok {
			selected = append(selected, e)
		}
	}
	return t.read(selected)
}

// Tail hydrates a bounded branch window; before and turn are validated against
// structural metadata before any historical body is decoded.
func (t *Transcript) Tail(leaf, before, turn string, limit int) (Tail, bool, error) {
	path := leafPath(t.entries, leaf)
	if turn != "" {
		found := false
		for _, r := range turnRanges(path) {
			part := path[r.start:r.end]
			if slices.ContainsFunc(part, func(e Entry) bool { return e.ID == turn && (isUserMessage(e) || e.ID == part[0].ID) }) {
				path, found = part, true
				break
			}
		}
		if !found {
			return Tail{}, false, nil
		}
	}
	if before != "" {
		at := slices.IndexFunc(path, func(e Entry) bool { return e.ID == before })
		if at < 0 {
			return Tail{}, false, nil
		}
		path = path[:at]
	}
	if len(path) == 0 {
		return Tail{Entries: []Entry{}}, true, nil
	}
	start := max(0, len(path)-ClampViewLimit(limit))
	for {
		bodies, err := t.read(withTurnOpeningUser(path, path[start:]))
		if err != nil {
			return Tail{}, false, err
		}
		out := slimPath(bodies, nil, toolsDigests{})
		raw, err := json.Marshal(out)
		if err != nil {
			return Tail{}, false, err
		}
		if len(raw) <= MaxViewPageBytes || start == len(path)-1 {
			return Tail{Entries: out, HasMore: start > 0, OldestID: path[start].ID}, true, nil
		}
		start += max(1, (len(path)-start)/2)
	}
}

// Compact selects visible bodies using structural metadata, then reprojects
// with only those bodies hydrated. Hidden replies keep their exact stats and
// node identities without retaining their prose, arguments or attachments.
func (t *Transcript) Compact(leaf, before, turn string, keep int) (CompactPage, bool, error) {
	var plan CompactPage
	found := true
	if turn != "" {
		plan, found = BuildCompactTurn(t.entries, leaf, turn, keep)
	} else {
		plan = BuildCompact(t.entries, leaf, before, keep)
	}
	if !found {
		return CompactPage{}, false, nil
	}
	bodies, err := t.read(plan.Entries)
	if err != nil {
		return CompactPage{}, false, err
	}
	byID := make(map[string]Entry, len(bodies))
	for _, e := range bodies {
		byID[e.ID] = e
	}
	entries := slices.Clone(t.entries)
	for i, e := range entries {
		if body, ok := byID[e.ID]; ok {
			entries[i] = body
		}
	}
	if turn != "" {
		page, ok := BuildCompactTurn(entries, leaf, turn, keep)
		return page, ok, nil
	}
	return BuildCompact(entries, leaf, before, keep), true, nil
}
