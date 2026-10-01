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
	ID                  string          `json:"id"`
	ParentID            string          `json:"parentId,omitempty"`
	TailID              string          `json:"tailId"`
	EntryCount          int             `json:"entryCount"`
	AssistantAt         int64           `json:"assistantAt,omitempty"`
	ToolStates          []TurnToolState `json:"toolStates"`
	CumulativeElapsedMS int64           `json:"cumulativeElapsedMs"`
	EntryIDs            []string        `json:"entryIds"`
	VisibleNodeIDs      []string        `json:"visibleNodeIds"`
	OmittedNodeIDs      []string        `json:"omittedNodeIds,omitempty"`
	HiddenCount         int             `json:"hiddenCount"`
	FirstHiddenID       string          `json:"firstHiddenId,omitempty"`
	Preview             string          `json:"preview,omitempty"`
	Stats               TurnStats       `json:"stats"`
	StepCount           int             `json:"stepCount"`
	LastStep            *TurnStep       `json:"lastStep,omitempty"`
}

// TurnToolState covers only the latest assistant tool batch, not the hidden
// history. It lets sparse snapshots deduplicate both running and completed
// tools against events received while the snapshot was in flight.
type TurnToolState struct {
	ID       string `json:"id"`
	Finished bool   `json:"finished"`
	IsError  bool   `json:"isError,omitempty"`
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
	StartedAt    int64    `json:"startedAt,omitempty"`
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

// isHumanTurnMessage distinguishes real inputs from runtime-authored user-role
// messages. Agent directives and completion notifications belong to the
// surrounding transcript turn so compact mode may fold them; extension origins
// still represent user input relayed from another client.
func isHumanTurnMessage(e Entry) bool {
	if !isUserMessage(e) {
		return false
	}
	origin := e.Message.Origin
	return origin == "" || strings.HasPrefix(origin, "extension:")
}

func turnRanges(path []Entry) []turnRange {
	var out []turnRange
	for i, e := range path {
		if isHumanTurnMessage(e) {
			if len(out) > 0 {
				out[len(out)-1].end = i
			}
			out = append(out, turnRange{i, len(path), len(out) + 1})
		}
	}
	// Imports can begin with an assistant/compaction before the first user.
	if len(out) == 0 && len(path) > 0 {
		out = append(out, turnRange{0, len(path), 1})
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
	elapsed := cumulativeTurnElapsed(path, ranges)
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
		turn.CumulativeElapsedMS = elapsed[i]
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
	ranges := turnRanges(path)
	elapsed := cumulativeTurnElapsed(path, ranges)
	for i, r := range ranges {
		part := path[r.start:r.end]
		if slices.ContainsFunc(part, func(e Entry) bool { return e.ID == id }) {
			prevPrompt, cacheReported := cacheBaseline(path[:r.start])
			turn, bodies, _, _ := projectTurn(part, r.ordinal, min(20, max(0, keep)), prevPrompt, cacheReported)
			turn.StepCount = completedSteps(path[:r.end])
			turn.CumulativeElapsedMS = elapsed[i]
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
		// alwaysVisible marks lifecycle metadata, not a reply. These rows stay
		// on their own and never count toward `keep`, otherwise a trailing
		// compaction/cancellation would hide the newest real reply.
		alwaysVisible bool
		suppressed    bool
	}
	var nodes []node
	tools := map[string]int{}
	failedTools := map[string]bool{}
	batch := map[string]int{}
	toolStates := []TurnToolState{}
	var assistantAt int64
	user := -1
	stats := TurnStats{Turn: ordinal}
	var lastStep *TurnStep
	var clock turnClock
	var decodeMS, decodeTokens int64
	pendingCompaction := -1
	pendingSummary := false
	checkpoints := map[string]bool{}
	for _, e := range path {
		if e.Type == "compaction" {
			checkpoints[e.ID] = true
		}
	}
	for i, e := range path {
		clock.add(e)
		// A new request cannot finish an interrupted earlier compaction;
		// inline compaction includes its assistant before the checkpoint.
		if isUserMessage(e) || e.Type == "request_header" {
			pendingCompaction, pendingSummary = -1, false
		}
		m := e.Message
		switch {
		case isHumanTurnMessage(e):
			user = i
		case isUserMessage(e):
			// Why: the runtime stores subagent traffic with role=user. It is
			// transcript output, not an always-visible human turn anchor.
			nodes = append(nodes, node{id: e.ID, preview: m.Text(), entries: []int{i}})
		case m != nil && m.Role == "assistant":
			batch = map[string]int{}
			toolStates = []TurnToolState{}
			lastStep = &TurnStep{Usage: m.Usage, TTFTMs: m.TTFTMs, LatencyMs: m.LatencyMs}
			// Legacy sessions stored run_aborted as an unparented sideband.
			// Keep their branch-correct aborted assistant visible so the client
			// can synthesize the standalone cancellation row.
			nodes = append(nodes, node{id: e.ID, preview: m.Text(), entries: []int{i}, alwaysVisible: m.StopReason == "aborted"})
			stats.Steps++
			stats.DurationMS += m.LatencyMs
			assistantAt = max(assistantAt, entryMillis(e))
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
					if classifyCacheMiss(int(prevPrompt), cacheReported, u) != nil {
						stats.CacheMisses++
					}
					prevPrompt = prompt
					cacheReported = cacheReported || read+write > 0
				}
			}
			for _, c := range m.Content {
				if c.Type == "toolCall" && c.ID != "" {
					if _, exists := batch[c.ID]; !exists {
						batch[c.ID] = len(toolStates)
						toolStates = append(toolStates, TurnToolState{ID: c.ID})
					}
					if at, ok := tools[c.ID]; ok {
						nodes[at].entries = append(nodes[at].entries, i)
					} else {
						tools[c.ID] = len(nodes)
						nodes = append(nodes, node{id: c.ID, preview: c.Name, entries: []int{i}})
					}
				}
			}
		case m != nil && m.Role == "toolResult":
			id := m.ToolCallID
			if id == "" {
				id = e.ID
			}
			if at, exists := batch[id]; exists {
				toolStates[at].Finished, toolStates[at].IsError = true, m.IsError
			} else {
				batch[id] = len(toolStates)
				toolStates = append(toolStates, TurnToolState{ID: id, Finished: true, IsError: m.IsError})
			}
			if m.IsError {
				failedTools[id] = true
			}
			if at, ok := tools[id]; ok {
				nodes[at].entries = append(nodes[at].entries, i)
			} else {
				tools[id] = len(nodes)
				nodes = append(nodes, node{id: id, preview: m.ToolName, entries: []int{i}})
			}
		case e.Type == "compaction":
			if pendingCompaction >= 0 {
				nodes[pendingCompaction].suppressed = true
				pendingSummary = true
			}
			if e.Usage != nil {
				lastStep = &TurnStep{Usage: e.Usage}
			}
			preview := e.Summary
			if e.RemoteContext || e.Responses != nil {
				preview = "Provider remote compaction"
			}
			nodes = append(nodes, node{id: e.ID, preview: preview, entries: []int{i}, alwaysVisible: true})
			stats.Steps++
			// A compaction rewrites the context, so the client restarts its
			// cache comparison here too.
			prevPrompt, cacheReported = 0, false
		case e.Type == "run_aborted":
			nodes = append(nodes, node{id: e.ID, preview: "Run cancelled", entries: []int{i}, alwaysVisible: true})
		case e.Type == "compaction_start":
			pendingCompaction, pendingSummary = len(nodes), false
			nodes = append(nodes, node{id: e.ID, preview: "Compacting", entries: []int{i}, alwaysVisible: true})
		case e.Type == "compaction_end":
			at := []int{i}
			if pendingCompaction >= 0 {
				nodes[pendingCompaction].suppressed = true
				at = append(slices.Clone(nodes[pendingCompaction].entries), i)
			}
			details, _ := e.Details.(map[string]any)
			summaryID, _ := details["entryId"].(string)
			if !pendingSummary && !checkpoints[summaryID] {
				nodes = append(nodes, node{id: e.ID, preview: "Compaction finished", entries: at, alwaysVisible: true})
			}
			pendingCompaction, pendingSummary = -1, false
		}
	}
	nodes = slices.DeleteFunc(nodes, func(n node) bool { return n.suppressed })
	stats.Tools = len(tools)
	stats.ToolFailures = len(failedTools)
	stats.StartedAt, stats.ElapsedMS = clock.start, clock.elapsed()
	if decodeMS > 0 {
		tps := float64(decodeTokens) / (float64(decodeMS) / 1000)
		stats.TPS = &tps
	}
	t := CompactTurn{ToolStates: toolStates, EntryCount: len(path), AssistantAt: assistantAt, ID: path[0].ID, ParentID: path[0].ParentID, TailID: path[len(path)-1].ID, Stats: stats, LastStep: lastStep, EntryIDs: []string{}, VisibleNodeIDs: []string{}}
	selected := map[int]bool{}
	// Empty/failed operations have no checkpoint body. Their lifecycle must
	// survive recovery too, without consuming a reply slot or a model step.
	// Keep both identities even when a checkpoint suppresses the status row.
	for i, e := range path {
		if e.Type == "compaction_start" || e.Type == "compaction_end" {
			selected[i] = true
		}
	}
	if user >= 0 {
		t.ID = path[user].ID
		selected[user] = true
		t.VisibleNodeIDs = append(t.VisibleNodeIDs, t.ID)
	} else if len(nodes) > 0 {
		// A subagent session can start with a machine-authored user message and
		// have no human input at all. Keep that first node's body as a hidden
		// anchor so the browser can attach the remote fold row to this turn.
		t.ID = nodes[0].id
		selected[nodes[0].entries[0]] = true
	}
	// Fold only real replies: the newest `keep` of them stay visible, and every
	// lifecycle metadata row stays visible too. Counting one toward `keep`
	// would make a trailing status hide the turn's final answer.
	var replies []int
	for i, n := range nodes {
		if !n.alwaysVisible {
			replies = append(replies, i)
		}
	}
	hiddenCount := max(0, len(replies)-keep)
	hidden := map[int]bool{}
	for _, i := range replies[:hiddenCount] {
		hidden[i] = true
	}
	t.HiddenCount = hiddenCount
	if hiddenCount > 0 {
		for i := range nodes {
			if hidden[i] {
				t.FirstHiddenID = nodes[i].id
				break
			}
		}
		// The preview summarises the newest folded reply, mirroring the fold
		// row the browser builds from the hidden nodes.
		newest := replies[hiddenCount-1]
		t.Preview = utf8Prefix(strings.Join(strings.Fields(nodes[newest].preview), " "), 160)
	}
	for i, n := range nodes {
		if hidden[i] {
			continue
		}
		t.VisibleNodeIDs = append(t.VisibleNodeIDs, n.id)
		for _, at := range n.entries {
			selected[at] = true
		}
	}
	// A visible tool may share its call entry with a hidden assistant or other
	// tools. Tell the client which helper nodes that retained entry must omit.
	for i, n := range nodes {
		if !hidden[i] {
			continue
		}
		if slices.ContainsFunc(n.entries, func(at int) bool { return selected[at] }) {
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

// turnClock shares one elapsed contract between per-turn and cumulative stats.
// Parallel tool results can be persisted in call order, not completion order;
// use the maximum completion time so a later sibling cannot shorten the span.
type turnClock struct {
	start, last, duration int64
	started, human        bool
}

func (c *turnClock) add(e Entry) {
	switch {
	case isHumanTurnMessage(e):
		if !c.human {
			c.start, c.started, c.human = entryMillis(e), true, true
		}
	case isUserMessage(e):
		// Runtime notifications belong to this turn; they must not reset its clock.
		if !c.started {
			c.start, c.started = entryMillis(e), true
		}
	case e.Message != nil && e.Message.Role == "assistant":
		c.duration += e.Message.LatencyMs
		c.last = max(c.last, entryMillis(e))
	case e.Message != nil && e.Message.Role == "toolResult", e.Type == "compaction":
		c.last = max(c.last, entryMillis(e))
	}
}

func (c turnClock) elapsed() int64 {
	if c.start > 0 && c.last > c.start {
		return c.last - c.start
	}
	return c.duration
}

func cumulativeTurnElapsed(path []Entry, ranges []turnRange) []int64 {
	out := make([]int64, len(ranges))
	var total int64
	for i, r := range ranges {
		var clock turnClock
		for _, e := range path[r.start:r.end] {
			clock.add(e)
		}
		total += clock.elapsed()
		out[i] = total
	}
	return out
}
