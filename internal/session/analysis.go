package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"strings"

	"ki/internal/types"
)

// TraceFilter selects rows from an active-leaf diagnostic trace.
type TraceFilter struct {
	Types       []string
	Roles       []string
	Tools       []string
	FailedOnly  bool
	CacheMisses bool
	Since       string
	Until       string
}

// CacheMiss describes how much of the preceding prompt was not served from
// cache. Compaction resets the baseline, so rebuilding a rewritten context is
// not reported as a miss.
type CacheMiss struct {
	MissedTokens int     `json:"missedTokens"`
	Ratio        float64 `json:"ratio"`
	Previous     int     `json:"previousPromptTokens"`
	Prompt       int     `json:"promptTokens"`
}

// TraceEntry is a compact, stable diagnostic projection of one transcript row.
type TraceEntry struct {
	ID            string       `json:"id"`
	ParentID      string       `json:"parentId,omitempty"`
	Timestamp     string       `json:"timestamp"`
	Type          string       `json:"type"`
	Role          string       `json:"role,omitempty"`
	Origin        string       `json:"origin,omitempty"`
	Preview       string       `json:"preview,omitempty"`
	ToolNames     []string     `json:"toolNames,omitempty"`
	ToolCallID    string       `json:"toolCallId,omitempty"`
	Provider      string       `json:"provider,omitempty"`
	Model         string       `json:"model,omitempty"`
	Usage         *types.Usage `json:"usage,omitempty"`
	PromptTokens  int          `json:"promptTokens,omitzero"`
	CacheMiss     *CacheMiss   `json:"cacheMiss,omitempty"`
	UsedTokens    int          `json:"usedTokens,omitzero"`
	ContextWindow int          `json:"contextWindow,omitzero"`
	Estimated     bool         `json:"estimated,omitzero"`
	LatencyMs     int64        `json:"latencyMs,omitzero"`
	DurationMs    int64        `json:"durationMs,omitzero"`
	Failed        bool         `json:"failed,omitzero"`
	Error         string       `json:"error,omitempty"`
	SystemHash    string       `json:"systemHash,omitempty"`
	ToolsHash     string       `json:"toolsHash,omitempty"`
	TokensBefore  int          `json:"tokensBefore,omitzero"`
	StopReason    string       `json:"stopReason,omitempty"`
}

// PromptChange records a model-visible request prefix change.
type PromptChange struct {
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp"`
	SystemHash string `json:"systemHash"`
	ToolsHash  string `json:"toolsHash"`
}

// ToolFailure is one failed tool result.
type ToolFailure struct {
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp"`
	Tool       string `json:"tool"`
	ToolCallID string `json:"toolCallId,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Analysis summarizes the active branch of a session.
type Analysis struct {
	Entries          int            `json:"entries"`
	UserTurns        int            `json:"userTurns"`
	AssistantSteps   int            `json:"assistantSteps"`
	ToolCalls        int            `json:"toolCalls"`
	ToolFailures     []ToolFailure  `json:"toolFailures"`
	Compactions      []TraceEntry   `json:"compactions"`
	CacheMisses      []TraceEntry   `json:"cacheMisses"`
	PromptChanges    []PromptChange `json:"promptChanges"`
	InputTokens      int64          `json:"inputTokens"`
	OutputTokens     int64          `json:"outputTokens"`
	CacheReadTokens  int64          `json:"cacheReadTokens"`
	CacheWriteTokens int64          `json:"cacheWriteTokens"`
	MaxUsedTokens    int            `json:"maxUsedTokens"`
	ContextWindow    int            `json:"contextWindow"`
}

// Trace returns an active-leaf event timeline with derived cache diagnostics.
func Trace(entries []Entry, leafID string, filter TraceFilter) []TraceEntry {
	path := LeafChain(entries, leafID)
	rows := make([]TraceEntry, 0, len(path))
	prevPrompt := 0
	cacheReported := false
	for _, e := range path {
		row := traceEntry(e)
		if e.Type == "compaction" {
			prevPrompt, cacheReported = 0, false
		} else if e.Message != nil && e.Message.Role == "assistant" && e.Message.Usage != nil {
			u := e.Message.Usage
			row.PromptTokens = u.Input + u.CacheRead + u.CacheWrite
			if row.PromptTokens > 0 {
				row.CacheMiss = classifyCacheMiss(prevPrompt, cacheReported, u)
				prevPrompt = row.PromptTokens
				cacheReported = cacheReported || u.CacheRead+u.CacheWrite > 0
			}
		}
		if traceMatches(row, filter) {
			rows = append(rows, row)
		}
	}
	return rows
}

func classifyCacheMiss(prevPrompt int, cacheReported bool, u *types.Usage) *CacheMiss {
	if u == nil {
		return nil
	}
	prompt := u.Input + u.CacheRead + u.CacheWrite
	if prevPrompt <= 0 || prompt <= 0 || (u.CacheRead+u.CacheWrite == 0 && !cacheReported) {
		return nil
	}
	missed := min(prevPrompt, prompt) - u.CacheRead
	if missed <= cacheMissNoiseFloor {
		return nil
	}
	ratio := float64(missed) / float64(prevPrompt)
	if missed < cacheMissNoticeTokens && ratio < cacheMissNoticeRatio {
		return nil
	}
	return &CacheMiss{MissedTokens: missed, Ratio: ratio, Previous: prevPrompt, Prompt: prompt}
}

// TraceWithContext includes neighboring rows around filter matches. It lets a
// diagnostic client fetch one bounded report instead of issuing one request
// per interesting entry.
func TraceWithContext(entries []Entry, leafID string, filter TraceFilter, contextLines int) []TraceEntry {
	if contextLines <= 0 {
		return Trace(entries, leafID, filter)
	}
	all := Trace(entries, leafID, TraceFilter{})
	keep := make([]bool, len(all))
	for i, row := range all {
		if !traceMatches(row, filter) {
			continue
		}
		for j := max(0, i-contextLines); j <= min(len(all)-1, i+contextLines); j++ {
			keep[j] = true
		}
	}
	out := make([]TraceEntry, 0)
	for i, row := range all {
		if keep[i] {
			out = append(out, row)
		}
	}
	return out
}

// Analyze summarizes the active leaf using the same cache rules as Trace and
// the compact WebUI projection.
func Analyze(entries []Entry, leafID string) Analysis {
	path := LeafChain(entries, leafID)
	trace := Trace(entries, leafID, TraceFilter{})
	out := Analysis{Entries: len(path)}
	lastSystem, lastTools := "", ""
	for _, row := range trace {
		switch {
		case row.Type == "compaction":
			out.Compactions = append(out.Compactions, row)
		case row.Type == "context_usage":
			out.MaxUsedTokens = max(out.MaxUsedTokens, row.UsedTokens)
			out.ContextWindow = max(out.ContextWindow, row.ContextWindow)
		case row.Type == "request_header":
			if row.SystemHash != lastSystem || row.ToolsHash != lastTools {
				out.PromptChanges = append(out.PromptChanges, PromptChange{
					ID: row.ID, Timestamp: row.Timestamp, SystemHash: row.SystemHash, ToolsHash: row.ToolsHash,
				})
				lastSystem, lastTools = row.SystemHash, row.ToolsHash
			}
		}
		if row.Role == "user" && row.Type == "message" && (row.Origin == "" || strings.HasPrefix(row.Origin, "extension:")) {
			out.UserTurns++
		}
		if row.Role == "assistant" {
			out.AssistantSteps++
		}
		if row.Role == "assistant" {
			out.ToolCalls += len(row.ToolNames)
		}
		if row.Failed {
			out.ToolFailures = append(out.ToolFailures, ToolFailure{
				ID: row.ID, Timestamp: row.Timestamp, Tool: first(row.ToolNames), ToolCallID: row.ToolCallID, Error: row.Error,
			})
		}
		if row.CacheMiss != nil {
			out.CacheMisses = append(out.CacheMisses, row)
		}
		if row.Usage != nil {
			out.InputTokens += int64(row.Usage.Input)
			out.OutputTokens += int64(row.Usage.Output)
			out.CacheReadTokens += int64(row.Usage.CacheRead)
			out.CacheWriteTokens += int64(row.Usage.CacheWrite)
		}
	}
	return out
}

func traceEntry(e Entry) TraceEntry {
	row := TraceEntry{
		ID: e.ID, ParentID: e.ParentID, Timestamp: e.Timestamp, Type: e.Type,
		Provider: e.Provider, Model: e.ModelID, UsedTokens: e.UsedTokens,
		ContextWindow: e.ContextWindow, Estimated: e.Estimated, TokensBefore: e.TokensBefore,
	}
	if e.Type == "request_header" {
		row.SystemHash = digest(e.System)
		row.ToolsHash = digestJSON(e.Tools)
	}
	if e.Type == "compaction" {
		if e.Responses != nil {
			row.Preview = "Provider remote compaction"
		} else {
			row.Preview = preview(e.Summary, 160)
		}
		row.Usage = e.Usage
	}
	if m := e.Message; m != nil {
		row.Role, row.Origin, row.Usage = m.Role, m.Origin, m.Usage
		row.LatencyMs, row.DurationMs = m.LatencyMs, m.DurationMs
		row.ToolCallID, row.StopReason = m.ToolCallID, m.StopReason
		row.Failed, row.Error = m.IsError || m.ErrorMessage != "", m.ErrorMessage
		if row.Error == "" && row.Failed {
			row.Error = preview(m.Text(), 240)
		}
		row.Preview = preview(messagePreview(*m), 160)
		for _, call := range m.ToolCalls() {
			row.ToolNames = append(row.ToolNames, call.Name)
		}
		if m.Role == "toolResult" && m.ToolName != "" {
			row.ToolNames = []string{m.ToolName}
		}
	}
	return row
}

func traceMatches(row TraceEntry, f TraceFilter) bool {
	if len(f.Types) > 0 && !slices.Contains(f.Types, row.Type) {
		return false
	}
	if len(f.Roles) > 0 && !slices.Contains(f.Roles, row.Role) {
		return false
	}
	if len(f.Tools) > 0 && !slices.ContainsFunc(row.ToolNames, func(name string) bool { return slices.Contains(f.Tools, name) }) {
		return false
	}
	if f.FailedOnly && !row.Failed {
		return false
	}
	if f.CacheMisses && row.CacheMiss == nil {
		return false
	}
	if f.Since != "" && row.Timestamp < f.Since {
		return false
	}
	return f.Until == "" || row.Timestamp <= f.Until
}

func messagePreview(m types.Message) string {
	if text := strings.TrimSpace(m.Text()); text != "" {
		return text
	}
	for _, c := range m.Content {
		if c.Thinking != "" {
			return c.Thinking
		}
		if c.Name != "" {
			return c.Name
		}
	}
	return m.ErrorMessage
}

func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func digestJSON(v any) string {
	b, _ := json.Marshal(v)
	return digest(string(b))
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// CacheMissPercent returns a display percentage without exposing floating
// point formatting choices to every client.
func CacheMissPercent(m *CacheMiss) int {
	if m == nil {
		return 0
	}
	return int(math.Round(m.Ratio * 100))
}
