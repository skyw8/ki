package session

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"ki/internal/types"
)

const DefaultViewLimit = 100

const MaxViewBytes = 24 * 1024

const MaxViewPageBytes = 512 * 1024

const toolPreviewBytes = 1024

const MaxViewEntries = 500

const MaxViewBatch = 40

const indexPreviewLen = 160

type IndexEntry struct {
	Type              string           `json:"type"`
	ID                string           `json:"id"`
	ParentID          string           `json:"parentId,omitempty"`
	Timestamp         string           `json:"timestamp,omitempty"`
	Role              string           `json:"role,omitempty"`
	Name              string           `json:"name,omitempty"`
	Preview           string           `json:"preview,omitempty"`
	ToolCallID        string           `json:"toolCallId,omitempty"`
	IsError           bool             `json:"isError,omitzero"`
	ParentCallID      string           `json:"parentCallId,omitempty"`
	CellID            string           `json:"cellId,omitempty"`
	RequestedToolName string           `json:"requestedToolName,omitempty"`
	Truncated         bool             `json:"truncated,omitzero"`
	Usage             *types.Usage     `json:"usage,omitempty"`
	DurationMs        int64            `json:"durationMs"`
	TTFTMs            int64            `json:"ttftMs,omitzero"`
	Origin            string           `json:"origin,omitempty"`
	Sideband          bool             `json:"sideband,omitzero"`
	TokensBefore      int              `json:"tokensBefore,omitzero"`
	StopReason        string           `json:"stopReason,omitempty"`
	RemoteContext     bool             `json:"remoteContext,omitempty"`
	ContextEstimate   *ContextEstimate `json:"contextEstimate,omitempty"`
}

type View struct {
	Index    []IndexEntry
	Entries  []Entry
	HasMore  bool
	OldestID string
}

type Tail struct {
	Entries  []Entry
	HasMore  bool
	OldestID string
}

func ClampViewLimit(n int) int {
	if n <= 0 {
		return DefaultViewLimit
	}
	if n > MaxViewEntries {
		return MaxViewEntries
	}
	return n
}

func BuildTail(entries []Entry, leafID string, limit int, complete bool) Tail {
	limit = ClampViewLimit(limit)
	path := leafPath(entries, leafID)
	slimmed, tailStart := boundedWindow(path, limit)

	oldest := ""
	if tailStart < len(path) {
		oldest = path[tailStart].ID
	} else if len(path) > 0 {
		oldest = path[0].ID
	}
	missingPrefix := !complete && (len(path) == 0 || path[0].ParentID != "")
	return Tail{Entries: slimmed, HasMore: tailStart > 0 || missingPrefix, OldestID: oldest}
}

func withTurnOpeningUser(path, keep []Entry) []Entry {
	first := firstChatEntry(keep)
	if first == nil || isUserMessage(*first) {
		return keep
	}
	user := userMessageBefore(path, first.ID)
	if user == nil || slices.ContainsFunc(keep, func(e Entry) bool { return e.ID == user.ID }) {
		return keep
	}
	want := make(map[string]bool, len(keep)+1)
	for _, e := range keep {
		want[e.ID] = true
	}
	want[user.ID] = true
	out := make([]Entry, 0, len(keep)+1)
	for _, e := range path {
		if want[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

func firstChatEntry(entries []Entry) *Entry {
	for i := range entries {
		switch entries[i].Type {
		case "message", "compaction", "compaction_start", "compaction_end", "run_aborted":
			return &entries[i]
		}
		if audit, ok := codeModeToolAudit(entries[i]); ok && audit["toolCallId"] != audit["parentCallId"] {
			return &entries[i]
		}
	}
	return nil
}

func userMessageBefore(path []Entry, id string) *Entry {
	at := -1
	for i := range path {
		if path[i].ID == id {
			at = i
			break
		}
	}
	for i := at - 1; i >= 0; i-- {
		if isUserMessage(path[i]) {
			return &path[i]
		}
	}
	return nil
}

func isUserMessage(e Entry) bool {
	return e.Type == "message" && e.Message != nil && e.Message.Role == "user"
}

func BuildIndex(entries []Entry) []IndexEntry {
	index := make([]IndexEntry, 0, len(entries))
	for _, e := range entries {
		index = append(index, indexOf(e))
	}
	return index
}

func LeafChain(entries []Entry, leaf string) []Entry {
	return leafPath(entries, leaf)
}

func BuildView(entries []Entry, leafID string, limit int) View {
	tail := BuildTail(entries, leafID, limit, true)
	return View{
		Index:    BuildIndex(entries),
		Entries:  tail.Entries,
		HasMore:  tail.HasMore,
		OldestID: tail.OldestID,
	}
}

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
	slimmed, start := boundedWindow(older, limit)
	oldest := older[start].ID

	return View{Entries: slimmed, HasMore: start > 0, OldestID: oldest}
}

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
			e = withContextEstimate(e)
			e.RemoteContext = e.RemoteContext || e.Responses != nil
			e.Responses = nil
			out = append(out, e)
		}
	}
	return out
}

func RedactProviderContext(entries []Entry) []Entry {
	out := slices.Clone(entries)
	for i := range out {
		out[i] = withContextEstimate(out[i])
		out[i].RemoteContext = out[i].RemoteContext || out[i].Responses != nil
		out[i].Responses = nil
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
	slices.Reverse(rev)
	return rev
}

func boundedWindow(path []Entry, limit int) ([]Entry, int) {
	if len(path) == 0 {
		return []Entry{}, 0
	}
	start := max(0, len(path)-limit)
	for {
		window := withTurnOpeningUser(path, path[start:])
		out := slimPath(window, nil, toolsDigests{})
		raw, _ := json.Marshal(out)
		if len(raw) <= MaxViewPageBytes || start == len(path)-1 {
			return out, start
		}
		start += max(1, (len(path)-start)/2)
	}
}

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
		out[i] = compactViewEntry(slimEntry(e, &prevSys, &prevTools, &seen, digests))
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
	out := withContextEstimate(e)
	out.RetainedTail = nil
	out.RemoteContext = out.RemoteContext || out.Responses != nil
	out.Responses = nil
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
		msg.Content = slices.Clone(msg.Content)
		truncated := false
		for i := range msg.Content {
			c := &msg.Content[i]
			c.ThinkingData = ""
			c.ThinkingSignature = ""
			c.TextSignature = ""
			if truncateString(&c.Text) {
				truncated = true
			}
			if msg.Role == "toolResult" && len(c.Text) > toolPreviewBytes {
				c.Text = utf8Prefix(c.Text, toolPreviewBytes)
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
		if audit, ok := codeModeToolAudit(out); ok {
			out.Details = slimCodeModeAudit(audit, MaxViewBytes)
		} else {
			out.Details = map[string]any{"truncated": true}
		}
		out.Truncated = true
	}
	return out
}

func indexOf(e Entry) IndexEntry {
	e = withContextEstimate(e)
	ix := IndexEntry{
		Type:            e.Type,
		ID:              e.ID,
		ParentID:        e.ParentID,
		Timestamp:       e.Timestamp,
		Sideband:        e.Sideband,
		TokensBefore:    e.TokensBefore,
		Usage:           e.Usage,
		RemoteContext:   e.RemoteContext || e.Responses != nil,
		ContextEstimate: e.ContextEstimate,
	}
	if e.Message != nil {
		ix.Role = e.Message.Role
		ix.Origin = e.Message.Origin
		ix.Usage = e.Message.Usage
		ix.DurationMs = e.Message.DurationMs
		ix.TTFTMs = e.Message.TTFTMs
		ix.StopReason = e.Message.StopReason
		ix.ToolCallID = e.Message.ToolCallID
		ix.IsError = e.Message.IsError
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
		if ix.RemoteContext {
			ix.Preview = "Provider remote compaction"
		} else {
			ix.Preview = previewOf(e.Summary)
		}
	}
	if audit, ok := codeModeToolAudit(e); ok {
		ix.ToolCallID, _ = audit["toolCallId"].(string)
		ix.Name, _ = audit["toolName"].(string)
		ix.ParentCallID, _ = audit["parentCallId"].(string)
		ix.CellID, _ = audit["cellId"].(string)
		ix.RequestedToolName, _ = audit["requestedToolName"].(string)
		ix.IsError, _ = audit["isError"].(bool)
		switch value := audit["durationMs"].(type) {
		case int:
			ix.DurationMs = int64(value)
		case int64:
			ix.DurationMs = value
		case float64:
			ix.DurationMs = int64(value)
		case json.Number:
			ix.DurationMs, _ = value.Int64()
		}
	}
	return ix
}

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

func compactViewEntry(e Entry) Entry {
	return compactViewEntryLimit(e, 64*1024)
}

func compactViewEntryLimit(e Entry, limit int) Entry {
	raw, _ := json.Marshal(e)
	if len(raw) <= limit {
		return e
	}
	audit, nestedAudit := codeModeToolAudit(e)
	e.Truncated = true
	e.Details, e.Pricing, e.Tools = nil, nil, nil
	e.System = utf8Prefix(e.System, 4096)
	e.Summary = utf8Prefix(e.Summary, 4096)
	if e.Message != nil {
		m := *e.Message
		m.Details, m.External = nil, nil
		m.Content = slices.Clone(m.Content[:min(len(m.Content), 32)])
		textLimit := min(256, max(32, limit/(max(1, len(m.Content))*8)))
		for i := range m.Content {
			c := &m.Content[i]
			c.Text = utf8Prefix(c.Text, textLimit)
			c.Thinking = utf8Prefix(c.Thinking, textLimit)
			c.Input = utf8Prefix(c.Input, textLimit)
			c.ArgumentsRaw = utf8Prefix(c.ArgumentsRaw, textLimit)
			c.Data, c.ThinkingData, c.ThinkingSignature, c.TextSignature = "", "", "", ""
			c.Arguments = nil
		}
		e.Message = &m
	}
	if nestedAudit {
		base, _ := json.Marshal(e)
		e.Details = slimCodeModeAudit(audit, max(0, limit-len(base)-32))
	}
	return e
}

func utf8Prefix(s string, size int) string {
	if len(s) <= size {
		return s
	}
	for size > 0 && !utf8.RuneStart(s[size]) {
		size--
	}
	return s[:size]
}
