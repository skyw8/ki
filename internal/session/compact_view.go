package session

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"ki/internal/types"
)

// CompactTurn describes a whole user turn without transferring its hidden
// replies. EntryIDs is the ordered, sparse body projection; ParentID/TailID
// bridge the omitted range without changing persisted parent edges.
type CompactTurn struct {
	ID             string    `json:"id"`
	ParentID       string    `json:"parentId,omitempty"`
	TailID         string    `json:"tailId"`
	EntryIDs       []string  `json:"entryIds"`
	VisibleNodeIDs []string  `json:"visibleNodeIds"`
	OmittedNodeIDs []string  `json:"omittedNodeIds,omitempty"`
	HiddenCount    int       `json:"hiddenCount"`
	FirstHiddenID  string    `json:"firstHiddenId,omitempty"`
	Preview        string    `json:"preview,omitempty"`
	Stats          TurnStats `json:"stats"`
	StepCount      int       `json:"stepCount"`
	LastStep       *TurnStep `json:"lastStep,omitempty"`
}

type TurnStep struct {
	Usage     *types.Usage `json:"usage,omitempty"`
	TTFTMs    int64        `json:"ttftMs"`
	LatencyMs int64        `json:"latencyMs"`
}

// TurnStats accounts for hidden steps too; folding changes presentation, never
// the usage or absolute turn number shown next to the final reply.
type TurnStats struct {
	Turn         int      `json:"turn"`
	Steps        int      `json:"steps"`
	ElapsedMS    int64    `json:"elapsedMs"`
	DurationMS   int64    `json:"durationMs"`
	Input        int64    `json:"input"`
	Output       int64    `json:"output"`
	CacheRead    int64    `json:"cacheRead"`
	CacheWrite   int64    `json:"cacheWrite"`
	Tools        int      `json:"tools"`
	ToolFailures int      `json:"toolFailures"`
	CacheMisses  int      `json:"cacheMisses"`
	HasCost      bool     `json:"hasCost"`
	Cost         float64  `json:"cost"`
	TTFTMS       int64    `json:"ttftMs"`
	TPS          *float64 `json:"tps"`
	Live         bool     `json:"live"`
}

type CompactPage struct {
	Entries  []Entry       `json:"entries"`
	Turns    []CompactTurn `json:"compactTurns"`
	HasMore  bool          `json:"hasMore"`
	OldestID string        `json:"oldestId"`
}

type turnRange struct{ start, end, ordinal int }

func turnRanges(path []Entry) []turnRange {
	var out []turnRange
	for i, e := range path {
		if isUserMessage(e) {
			if len(out) > 0 {
				out[len(out)-1].end = i
			}
			out = append(out, turnRange{i, len(path), len(out) + 1})
		}
	}
	// Imports can begin with an assistant/compaction before the first user.
	if len(out) == 0 && len(path) > 0 {
		out = append(out, turnRange{0, len(path), 0})
	} else if len(out) > 0 && out[0].start > 0 {
		out[0].start = 0
	}
	return out
}

// BuildCompact returns whole turns: four on open, exactly one before a cursor.
// Paging raw entries made a long folded turn grow on every upward gesture,
// without revealing any older prompt. The cursor must cross that whole turn.
func BuildCompact(entries []Entry, leaf, before string, keep int) CompactPage {
	path := leafPath(entries, leaf)
	ranges := turnRanges(path)
	end := len(ranges)
	if before != "" {
		end = 0
		for i, r := range ranges {
			if slices.ContainsFunc(path[r.start:r.end], func(e Entry) bool { return e.ID == before }) {
				end = i
				break
			}
		}
	}
	page := CompactPage{Entries: []Entry{}, Turns: []CompactTurn{}}
	count := 4
	if before != "" {
		count = 1
	}
	start := max(0, end-count)
	base := 0
	if start < len(ranges) {
		base = ranges[start].start
	}
	prevPrompt, cacheReported := cacheBaseline(path[:base])
	for i := start; i < end; i++ {
		r := ranges[i]
		turn, bodies, nextPrompt, nextReported := projectTurn(path[r.start:r.end], r.ordinal, min(20, max(0, keep)), prevPrompt, cacheReported)
		prevPrompt, cacheReported = nextPrompt, nextReported
		turn.StepCount = completedSteps(path[:r.end])
		page.Turns = append(page.Turns, turn)
		page.Entries = append(page.Entries, bodies...)
	}
	// Remove whole oldest turns to meet the page budget; never cut a turn.
	for len(page.Turns) > 1 {
		raw, _ := json.Marshal(page)
		if len(raw) <= MaxViewPageBytes {
			break
		}
		page.Entries = page.Entries[len(page.Turns[0].EntryIDs):]
		page.Turns = page.Turns[1:]
		start++
	}
	if len(page.Turns) > 0 {
		page.OldestID = page.Turns[0].ID
		page.HasMore = start > 0
	}
	return page
}

// BuildTurn pages the contents of an explicitly expanded turn. Its hasMore is
// local to that fold and must never advance the main history cursor.
func BuildTurn(entries []Entry, leaf, id, before string, limit int) (Tail, bool) {
	path := leafPath(entries, leaf)
	for _, r := range turnRanges(path) {
		part := path[r.start:r.end]
		if !slices.ContainsFunc(part, func(e Entry) bool { return e.ID == id && (isUserMessage(e) || e.ID == part[0].ID) }) {
			continue
		}
		if before != "" {
			cut := slices.IndexFunc(part, func(e Entry) bool { return e.ID == before })
			if cut < 0 {
				return Tail{}, false
			}
			part = part[:cut]
		}
		return BuildTail(part, "", limit, true), true
	}
	return Tail{}, false
}

// BuildCompactTurn reprojects one known turn when the browser changes its
// display preference. An entry inside a partial detailed page also identifies
// its enclosing turn, so switching modes can repair the boundary atomically.
func BuildCompactTurn(entries []Entry, leaf, id string, keep int) (CompactPage, bool) {
	path := leafPath(entries, leaf)
	for _, r := range turnRanges(path) {
		part := path[r.start:r.end]
		if slices.ContainsFunc(part, func(e Entry) bool { return e.ID == id }) {
			prevPrompt, cacheReported := cacheBaseline(path[:r.start])
			turn, bodies, _, _ := projectTurn(part, r.ordinal, min(20, max(0, keep)), prevPrompt, cacheReported)
			turn.StepCount = completedSteps(path[:r.end])
			return CompactPage{Entries: bodies, Turns: []CompactTurn{turn}, HasMore: r.ordinal > 1, OldestID: turn.ID}, true
		}
	}
	return CompactPage{}, false
}

// Prompt-cache miss thresholds mirror the client's `cacheMisses` so a folded
// turn's count matches the per-step badges for turns the browser computes.
const (
	cacheMissNoiseFloor   = 1024
	cacheMissNoticeTokens = 20_000
	cacheMissNoticeRatio  = 0.5
)

func projectTurn(path []Entry, ordinal, keep int, prevPrompt int64, cacheReported bool) (CompactTurn, []Entry, int64, bool) {
	type node struct {
		id, preview string
		entries     []int
	}
	var nodes []node
	tools := map[string]int{}
	failedTools := map[string]bool{}
	user := -1
	stats := TurnStats{Turn: ordinal}
	var lastStep *TurnStep
	var start, last, decodeMS, decodeTokens int64
	for i, e := range path {
		m := e.Message
		switch {
		case isUserMessage(e):
			user = i
			start = entryMillis(e)
		case m != nil && m.Role == "assistant":
			lastStep = &TurnStep{Usage: m.Usage, TTFTMs: m.TTFTMs, LatencyMs: m.LatencyMs}
			nodes = append(nodes, node{e.ID, m.Text(), []int{i}})
			stats.Steps++
			stats.DurationMS += m.LatencyMs
			last = entryMillis(e)
			if stats.TTFTMS == 0 && m.TTFTMs > 0 {
				stats.TTFTMS = m.TTFTMs
			}
			if u := m.Usage; u != nil {
				stats.Input += int64(u.Input + u.CacheRead + u.CacheWrite)
				stats.Output += int64(u.Output)
				stats.CacheRead += int64(u.CacheRead)
				stats.CacheWrite += int64(u.CacheWrite)
				if u.Cost != nil {
					stats.HasCost = true
					stats.Cost += u.Cost.Total
				}
				if m.TTFTMs > 0 && m.LatencyMs > m.TTFTMs && u.Output > 0 {
					decodeMS += m.LatencyMs - m.TTFTMs
					decodeTokens += int64(u.Output)
				}
				read, write := int64(u.CacheRead), int64(u.CacheWrite)
				prompt := int64(u.Input) + read + write
				if prompt > 0 {
					if prevPrompt > 0 && (read+write > 0 || cacheReported) {
						missed := min(prevPrompt, prompt) - read
						if missed > cacheMissNoiseFloor {
							ratio := float64(missed) / float64(prevPrompt)
							if missed >= cacheMissNoticeTokens || ratio >= cacheMissNoticeRatio {
								stats.CacheMisses++
							}
						}
					}
					prevPrompt = prompt
					cacheReported = cacheReported || read+write > 0
				}
			}
			for _, c := range m.Content {
				if c.Type == "toolCall" && c.ID != "" {
					if at, ok := tools[c.ID]; ok {
						nodes[at].entries = append(nodes[at].entries, i)
					} else {
						tools[c.ID] = len(nodes)
						nodes = append(nodes, node{c.ID, c.Name, []int{i}})
					}
				}
			}
		case m != nil && m.Role == "toolResult":
			id := m.ToolCallID
			if id == "" {
				id = e.ID
			}
			if m.IsError {
				failedTools[id] = true
			}
			if at, ok := tools[id]; ok {
				nodes[at].entries = append(nodes[at].entries, i)
			} else {
				tools[id] = len(nodes)
				nodes = append(nodes, node{id, m.ToolName, []int{i}})
			}
		case e.Type == "compaction":
			if e.Usage != nil {
				lastStep = &TurnStep{Usage: e.Usage}
			}
			nodes = append(nodes, node{e.ID, e.Summary, []int{i}})
			stats.Steps++
			// A compaction rewrites the context, so the client restarts its
			// cache comparison here too.
			prevPrompt, cacheReported = 0, false
		}
	}
	stats.Tools = len(tools)
	stats.ToolFailures = len(failedTools)
	stats.ElapsedMS = stats.DurationMS
	if start > 0 && last > start {
		stats.ElapsedMS = last - start
	}
	if decodeMS > 0 {
		tps := float64(decodeTokens) / (float64(decodeMS) / 1000)
		stats.TPS = &tps
	}
	t := CompactTurn{ID: path[0].ID, ParentID: path[0].ParentID, TailID: path[len(path)-1].ID, Stats: stats, LastStep: lastStep, EntryIDs: []string{}, VisibleNodeIDs: []string{}}
	selected := map[int]bool{}
	if user >= 0 {
		t.ID = path[user].ID
		selected[user] = true
		t.VisibleNodeIDs = append(t.VisibleNodeIDs, t.ID)
	}
	cut := max(0, len(nodes)-keep)
	t.HiddenCount = cut
	if cut > 0 {
		t.FirstHiddenID = nodes[0].id
		t.Preview = utf8Prefix(strings.Join(strings.Fields(nodes[cut-1].preview), " "), 160)
	}
	for _, n := range nodes[cut:] {
		t.VisibleNodeIDs = append(t.VisibleNodeIDs, n.id)
		for _, i := range n.entries {
			selected[i] = true
		}
	}
	// A visible tool may share its call entry with a hidden assistant or other
	// tools. Tell the client which helper nodes that retained entry must omit.
	for _, n := range nodes[:cut] {
		if slices.ContainsFunc(n.entries, func(i int) bool { return selected[i] }) {
			t.OmittedNodeIDs = append(t.OmittedNodeIDs, n.id)
		}
	}
	// Keep metadata needed by the composer even when the last reply is folded.
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Type == "context_usage" {
			selected[i] = true
			break
		}
	}
	var bodies []Entry
	for i, e := range path {
		if selected[i] {
			t.EntryIDs = append(t.EntryIDs, e.ID)
			bodies = append(bodies, e)
		}
	}
	bodies = slimPath(bodies, nil, toolsDigests{})
	for i, e := range bodies {
		if e.Message != nil && e.Message.Role == "assistant" && !slices.Contains(t.VisibleNodeIDs, e.ID) {
			// A visible tool needs its call, not the hidden assistant prose or
			// sibling calls stored in the same immutable entry.
			m := *e.Message
			m.Content = slices.DeleteFunc(slices.Clone(m.Content), func(c types.Content) bool {
				return c.Type != "toolCall" || !slices.Contains(t.VisibleNodeIDs, c.ID)
			})
			e.Message, e.Truncated = &m, true
		}
		// Many visible replies can together exceed a page even though each
		// fits the ordinary entry cap. Slim bodies further, never split a turn.
		bodies[i] = compactViewEntryLimit(e, MaxViewPageBytes/(len(bodies)+2))
	}
	return t, bodies, prevPrompt, cacheReported
}

// cacheBaseline replays the prompt-cache comparison over the turns preceding a
// projected page, so a folded turn's miss count matches the client's
// `cacheMisses` when the browser never sees those steps. It mirrors the
// assistant/compaction handling in projectTurn and returns the carried state.
func cacheBaseline(path []Entry) (int64, bool) {
	var prevPrompt int64
	reported := false
	for _, e := range path {
		if e.Type == "compaction" {
			prevPrompt, reported = 0, false
			continue
		}
		m := e.Message
		if m == nil || m.Role != "assistant" || m.Usage == nil {
			continue
		}
		read, write := int64(m.Usage.CacheRead), int64(m.Usage.CacheWrite)
		prompt := int64(m.Usage.Input) + read + write
		if prompt <= 0 {
			continue
		}
		prevPrompt = prompt
		reported = reported || read+write > 0
	}
	return prevPrompt, reported
}

func completedSteps(path []Entry) int {
	steps := 0
	for _, e := range path {
		if (e.Message != nil && e.Message.Role == "assistant") || (e.Type == "compaction" && e.Usage != nil) {
			steps++
		}
	}
	return steps
}

func entryMillis(e Entry) int64 {
	if e.Message != nil && e.Message.Timestamp > 0 {
		return e.Message.Timestamp
	}
	t, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
