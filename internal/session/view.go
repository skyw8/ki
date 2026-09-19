package session

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"ki/internal/types"
)

// DefaultViewLimit is the leaf-path tail returned as full (slimmed) entries.
const DefaultViewLimit = 100

// MaxViewBytes caps text-like fields in the WebUI session view.
const MaxViewBytes = 24 * 1024

// MaxViewEntries is the upper bound for limit= on GET /v1/sessions/{id}.
const MaxViewEntries = 500

// MaxViewBatch is the maximum number of ids accepted by ?entries=.
const MaxViewBatch = 40

const indexPreviewLen = 160

// IndexEntry is a body-less row used for branches, stats, and the trajectory table.
type IndexEntry struct {
	Type         string       `json:"type"`
	ID           string       `json:"id"`
	ParentID     string       `json:"parentId,omitempty"`
	Timestamp    string       `json:"timestamp,omitempty"`
	Role         string       `json:"role,omitempty"`
	Name         string       `json:"name,omitempty"`
	Preview      string       `json:"preview,omitempty"`
	ToolCallID   string       `json:"toolCallId,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
	Usage        *types.Usage `json:"usage,omitempty"`
	DurationMs   int64        `json:"durationMs"` // not omitempty: a fast tool reports a real 0ms
	TTFTMs       int64        `json:"ttftMs,omitempty"`
	Origin       string       `json:"origin,omitempty"`
	Sideband     bool         `json:"sideband,omitempty"`
	TokensBefore int          `json:"tokensBefore,omitempty"`
	StopReason   string       `json:"stopReason,omitempty"`
}

// View is the WebUI projection of one session: a full-tree index plus a slimmed leaf tail.
type View struct {
	Index    []IndexEntry
	Entries  []Entry
	HasMore  bool
	OldestID string
}

// Tail is the WebUI's conversation view: the slimmed tail of the active leaf
// plus the cursor for paging further back. It is what a session open needs, and
// it deliberately excludes the tree index so first paint costs the tail rather
// than the whole history.
type Tail struct {
	Entries  []Entry
	HasMore  bool
	OldestID string
}

// ClampViewLimit normalizes the GET limit query. 0 or negative uses the default.
func ClampViewLimit(n int) int {
	if n <= 0 {
		return DefaultViewLimit
	}
	if n > MaxViewEntries {
		return MaxViewEntries
	}
	return n
}

// BuildTail returns a slimmed window of the active leaf, oldest entry first.
func BuildTail(entries []Entry, leafID string, limit int) Tail {
	limit = ClampViewLimit(limit)
	path := leafPath(entries, leafID)
	keep, tailStart := selectLeafEntries(path, limit, toolsDigests{})
	slimmed := slimPath(keep, nil, toolsDigests{})
	oldest := ""
	if tailStart < len(path) {
		oldest = path[tailStart].ID
	} else if len(path) > 0 {
		oldest = path[0].ID
	}
	return Tail{Entries: slimmed, HasMore: tailStart > 0, OldestID: oldest}
}

// BuildIndex returns a body-less row per entry, in file order.
func BuildIndex(entries []Entry) []IndexEntry {
	index := make([]IndexEntry, 0, len(entries))
	for _, e := range entries {
		index = append(index, indexOf(e))
	}
	return index
}

// LeafChain returns the entries of the active branch, root first.
//
// The leaf resolution matches BuildView: an empty leafID means the newest
// non-sideband entry. Entries that are not in the given slice are ignored, so a
// caller holding only a window of the transcript gets the visible part of the
// branch instead of an error.
func LeafChain(entries []Entry, leaf string) []Entry {
	return leafPath(entries, leaf)
}

// BuildView returns an index of every entry and a slimmed tail of the active leaf.
func BuildView(entries []Entry, leafID string, limit int) View {
	tail := BuildTail(entries, leafID, limit)
	return View{
		Index:    BuildIndex(entries),
		Entries:  tail.Entries,
		HasMore:  tail.HasMore,
		OldestID: tail.OldestID,
	}
}

// BuildBefore returns slimmed leaf entries strictly older than beforeID.
func BuildBefore(entries []Entry, leafID, beforeID string, limit int) View {
	limit = ClampViewLimit(limit)
	path := leafPath(entries, leafID)
	cut := -1
	for i, e := range path {
		if e.ID == beforeID {
			cut = i
			break
		}
	}
	if cut <= 0 {
		return View{Entries: []Entry{}, HasMore: false}
	}
	older := path[:cut]
	start := 0
	if len(older) > limit {
		start = len(older) - limit
	}
	window := older[start:]
	slimmed := slimPath(window, older[:start], toolsDigests{})
	oldest := ""
	if len(window) > 0 {
		oldest = window[0].ID
	}
	return View{Entries: slimmed, HasMore: start > 0, OldestID: oldest}
}

// LookupEntries returns unslimmed entries for the given ids, preserving request order.
func LookupEntries(entries []Entry, ids []string) []Entry {
	if len(ids) > MaxViewBatch {
		ids = ids[:MaxViewBatch]
	}
	byID := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	out := make([]Entry, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

func leafPath(entries []Entry, leaf string) []Entry {
	byID := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	if leaf == "" {
		for i := len(entries) - 1; i >= 0; i-- {
			if !entries[i].Sideband {
				leaf = entries[i].ID
				break
			}
		}
	}
	var rev []Entry
	seen := map[string]bool{}
	id := leaf
	for id != "" && !seen[id] {
		seen[id] = true
		e, ok := byID[id]
		if !ok {
			break
		}
		rev = append(rev, e)
		id = e.ParentID
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

func selectLeafEntries(path []Entry, limit int, digests toolsDigests) ([]Entry, int) {
	if len(path) <= limit {
		return path, 0
	}
	tailStart := len(path) - limit
	keep := make(map[string]bool, limit+8)
	for _, e := range path[tailStart:] {
		keep[e.ID] = true
	}
	var prevSys string
	var prevTools string
	for _, e := range path {
		if e.Type != "request_header" {
			continue
		}
		tools := digests.key(e)
		if e.System != prevSys || tools != prevTools {
			keep[e.ID] = true
			prevSys = e.System
			prevTools = tools
		}
	}
	out := make([]Entry, 0, len(keep))
	for _, e := range path {
		if keep[e.ID] {
			out = append(out, e)
		}
	}
	return out, tailStart
}

// toolsDigests memoizes the tool-schema fingerprint of a request_header within
// one view build. Why: marshaling a full tool schema is the most expensive part
// of slimming a header, and both the keep-selection and the slim pass compare
// the same headers against their predecessor.
type toolsDigests map[string]string

func (d toolsDigests) key(e Entry) string {
	if k, ok := d[e.ID]; ok {
		return k
	}
	k := toolsKey(e.Tools)
	d[e.ID] = k
	return k
}

func slimPath(path, prior []Entry, digests toolsDigests) []Entry {
	prevSys, prevTools, seen := promptCursor(prior)
	out := make([]Entry, len(path))
	for i, e := range path {
		out[i] = slimEntry(e, &prevSys, &prevTools, &seen, digests)
	}
	return out
}

func promptCursor(entries []Entry) (string, string, bool) {
	var sys, tools string
	seen := false
	for _, e := range entries {
		if e.Type != "request_header" {
			continue
		}
		sys = e.System
		tools = toolsKey(e.Tools)
		seen = true
	}
	return sys, tools, seen
}

func slimEntry(e Entry, prevSys, prevTools *string, seenHeader *bool, digests toolsDigests) Entry {
	out := e
	out.RetainedTail = nil
	if out.Type == "request_header" {
		key := digests.key(out)
		if *seenHeader && *prevSys == out.System && *prevTools == key {
			out.System = ""
			out.Tools = nil
			out.PromptUnchanged = true
			return out
		}
		*seenHeader = true
		*prevSys = out.System
		*prevTools = key
		return out
	}
	if out.Message != nil {
		msg := *out.Message
		msg.Content = append([]types.Content(nil), msg.Content...)
		truncated := false
		for i := range msg.Content {
			c := &msg.Content[i]
			c.ThinkingData = ""
			c.ThinkingSignature = ""
			c.TextSignature = ""
			if truncateString(&c.Text) {
				truncated = true
			}
			if truncateString(&c.Thinking) {
				truncated = true
			}
			if truncateString(&c.Input) {
				truncated = true
			}
			if truncateString(&c.ArgumentsRaw) {
				truncated = true
			}
			if tooBig(c.Arguments) {
				c.Arguments = map[string]any{"truncated": true}
				truncated = true
			}
		}
		if tooBig(msg.Details) {
			msg.Details = map[string]any{"truncated": true}
			truncated = true
		}
		out.Message = &msg
		out.Truncated = truncated
	}
	if out.Summary != "" && truncateString(&out.Summary) {
		out.Truncated = true
	}
	if tooBig(out.Details) {
		out.Details = map[string]any{"truncated": true}
		out.Truncated = true
	}
	return out
}

func indexOf(e Entry) IndexEntry {
	ix := IndexEntry{
		Type:         e.Type,
		ID:           e.ID,
		ParentID:     e.ParentID,
		Timestamp:    e.Timestamp,
		Sideband:     e.Sideband,
		TokensBefore: e.TokensBefore,
		Usage:        e.Usage,
	}
	if e.Message != nil {
		ix.Role = e.Message.Role
		ix.Origin = e.Message.Origin
		ix.Usage = e.Message.Usage
		ix.DurationMs = e.Message.DurationMs
		ix.TTFTMs = e.Message.TTFTMs
		ix.StopReason = e.Message.StopReason
		ix.ToolCallID = e.Message.ToolCallID
		ix.Name = e.Message.ToolName
		text := e.Message.Text()
		if text == "" {
			for _, c := range e.Message.Content {
				if c.Type == "thinking" && (c.Thinking != "" || c.Text != "") {
					text = c.Thinking
					if text == "" {
						text = c.Text
					}
					break
				}
				if c.Type == "toolCall" && c.Name != "" {
					if ix.Name == "" {
						ix.Name = c.Name
					}
					if text == "" {
						text = c.Name
					}
				}
			}
		}
		if ix.Name == "" && e.Message.Role == "toolResult" {
			ix.Name = e.Message.ToolName
		}
		ix.Preview = previewOf(text)
		if len(e.Message.Text()) > indexPreviewLen {
			ix.Truncated = true
		}
	}
	switch e.Type {
	case "request_header":
		ix.Preview = previewOf(e.System)
	case "compaction":
		ix.Preview = previewOf(e.Summary)
	}
	return ix
}

// previewScanBytes bounds how much of a body the index preview looks at. Why:
// previewOf used to split every field of the whole body — a 300 KB tool result
// becomes a slice of ~50k words — just to keep 160 runes, which dominated the
// index build on long transcripts.
const previewScanBytes = 4 * 1024

func previewOf(text string) string {
	if len(text) > previewScanBytes {
		text = cutBytes(text, previewScanBytes)
	}
	t := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(t) <= indexPreviewLen {
		return t
	}
	return string([]rune(t)[:indexPreviewLen]) + "…"
}

func truncateString(s *string) bool {
	if s == nil || len(*s) <= MaxViewBytes {
		return false
	}
	*s = cutBytes(*s, MaxViewBytes)
	return true
}

func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

func tooBig(v any) bool {
	if v == nil {
		return false
	}
	if s, ok := v.(string); ok {
		return len(s) > MaxViewBytes
	}
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return len(b) > MaxViewBytes
}

func toolsKey(tools []ToolSchema) string {
	if tools == nil {
		return "null"
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return ""
	}
	return string(b)
}
