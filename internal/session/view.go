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
	DurationMs   int64        `json:"durationMs,omitempty"`
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

// BuildView returns an index of every entry and a slimmed tail of the active leaf.
func BuildView(entries []Entry, leafID string, limit int) View {
	limit = ClampViewLimit(limit)
	index := make([]IndexEntry, 0, len(entries))
	for _, e := range entries {
		index = append(index, indexOf(e))
	}
	path := leafPath(entries, leafID)
	keep, tailStart := selectLeafEntries(path, limit)
	slimmed := slimPath(keep, nil)
	oldest := ""
	if tailStart < len(path) {
		oldest = path[tailStart].ID
	} else if len(path) > 0 {
		oldest = path[0].ID
	}
	return View{
		Index:    index,
		Entries:  slimmed,
		HasMore:  tailStart > 0,
		OldestID: oldest,
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
	slimmed := slimPath(window, older[:start])
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

func selectLeafEntries(path []Entry, limit int) ([]Entry, int) {
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
		tools := toolsKey(e.Tools)
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

func slimPath(path, prior []Entry) []Entry {
	prevSys, prevTools, seen := promptCursor(prior)
	out := make([]Entry, len(path))
	for i, e := range path {
		out[i] = slimEntry(e, &prevSys, &prevTools, &seen)
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

func slimEntry(e Entry, prevSys, prevTools *string, seenHeader *bool) Entry {
	out := e
	out.RetainedTail = nil
	if out.Type == "request_header" {
		key := toolsKey(out.Tools)
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

func previewOf(text string) string {
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
