package compact

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ki/internal/config"
	"ki/internal/session"
	"ki/internal/types"
)

// Summarizer produces a structured summary. compact builds the full prompt
// (system + user), aligned with pi: SUMMARIZATION / UPDATE / TURN_PREFIX.
type Summarizer interface {
	Summarize(ctx context.Context, system, user string) (string, *types.Usage, error)
}

// EstimateTokens estimates context tokens for messages, preferring real usage
// (input + cacheRead, aligned with pi calculateContextTokens) from the newest
// assistant message. recentCompaction is the unix-ms timestamp of the latest
// compaction entry: usage from a message older than it reflects the
// pre-compaction (larger) context and would falsely re-trigger compaction right
// after one finished — fall back to the char/4 lower-bound estimate instead.

// ErrNothingToCompact reports that Prepare found no messages worth summarizing.
// The compaction is skipped and no model call happens.
var ErrNothingToCompact = errors.New("nothing to compact")

// EstimateTokens estimates the model-facing context size. Primary source is
// the newest assistant message with usage (pi calculateContextTokens:
// totalTokens, falling back to input+output+cacheRead+cacheWrite), plus
// char/4 for any trailing messages after it (pi trailingTokens). Falls back
// to pure char/4 when no usable usage exists. recentCompaction guards stale
// usage: a usage recorded before the last compaction reflects the old,
// larger context and would falsely re-trigger compaction.
func EstimateTokens(msgs []types.Message, recentCompaction int64) int {
	for i, v := range slices.Backward(msgs) {
		m := v
		if m.Role != "assistant" || m.Usage == nil {
			continue
		}
		if recentCompaction == 0 || m.Timestamp > recentCompaction {
			if t := m.Usage.TotalTokens; t > 0 {
				return t + trailingTokens(msgs[i+1:])
			}
			if t := m.Usage.Input + m.Usage.Output + m.Usage.CacheRead + m.Usage.CacheWrite; t > 0 {
				return t + trailingTokens(msgs[i+1:])
			}
		}
		break
	}
	return char4(msgs)
}

func trailingTokens(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		n += (len(m.Text()) + 3) / 4
	}
	return n
}

func char4(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		n += (len(m.Text()) + 3) / 4
	}
	return n
}

// ShouldRun reports auto-compaction.
func ShouldRun(tokens, contextWindow int, cfg config.Compaction) bool {
	if !cfg.Enabled {
		return false
	}
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	// Global floor: MaxContextTokens caps the model window so a small value
	// triggers compaction early (e.g. low-cost testing). Min semantics: the
	// model window never inflates past the configured cap.
	if cfg.MaxContextTokens > 0 && cfg.MaxContextTokens < contextWindow {
		contextWindow = cfg.MaxContextTokens
	}
	return tokens > contextWindow-cfg.ReserveTokens
}

// Preparation is the pure-function output of Prepare, ready for Execute
// (aligned with pi CompactionPreparation: cut, segments, iterative summary).
type Preparation struct {
	FirstKeptEntryID    string
	MessagesToSummarize []types.Message
	TurnPrefixMessages  []types.Message
	RetainedTail        []types.Message
	IsSplitTurn         bool
	TokensBefore        int
	PreviousSummary     string
}

// Prepare computes everything needed for one compaction without calling the
// model or writing anything. entries must be the leaf-chain entries
// (root → leaf, session.LeafEntries). It finds the previous compaction for the
// incremental summary, virtually expands its retained tail into the cut search
// (pi virtualRetainedEntries), picks a cut point that never splits a tool
// result, and detects a split turn (cut inside a turn → prefix summarized
// separately).
func Prepare(entries []session.Entry, cfg config.Compaction) (*Preparation, error) {
	keep := cfg.KeepRecentTokens
	if keep <= 0 {
		keep = 20000
	}
	prevCompIdx := -1
	for i, v := range slices.Backward(entries) {
		if v.Type == "compaction" {
			prevCompIdx = i
			break
		}
	}
	previousSummary := ""
	lastCompactionAt := int64(0)
	var virtual []session.Entry
	if prevCompIdx >= 0 {
		previousSummary = entries[prevCompIdx].Summary
		if t, err := parseTime(entries[prevCompIdx].Timestamp); err == nil {
			lastCompactionAt = t
		}
		for i, m := range entries[prevCompIdx].RetainedTail {
			virtual = append(virtual, session.Entry{
				Type:    "message",
				ID:      fmt.Sprintf("%s:retained:%d", entries[prevCompIdx].ID, i),
				Message: &m,
			})
		}
	}
	compactable := append(append([]session.Entry{}, virtual...), entries[prevCompIdx+1:]...)

	// tokensBefore: previous summary + all messages, usage-aware with the
	// stale guard (usage from before the last compaction is ignored).
	msgs := []types.Message{}
	if previousSummary != "" {
		msgs = append(msgs, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "Previous conversation summary:\n" + previousSummary}}})
	}
	for _, e := range compactable {
		if e.Type == "message" && e.Message != nil {
			msgs = append(msgs, *e.Message)
		}
	}
	tokensBefore := EstimateTokens(msgs, lastCompactionAt)

	// Valid cut points: user/assistant messages; never a toolResult (a kept
	// toolResult without its toolCall is semantically broken, pi
	// findValidCutPoints).
	var cutPoints []int
	for i, e := range compactable {
		if e.Type == "message" && e.Message != nil && e.Message.Role != "toolResult" {
			cutPoints = append(cutPoints, i)
		}
	}
	if len(cutPoints) == 0 {
		return nil, ErrNothingToCompact
	}

	// Walk newest → oldest until the recent-token budget is filled; cut at the
	// earliest valid cut point not older than the budget position.
	acc := 0
	cutIndex := cutPoints[0]
	for i, v := range slices.Backward(compactable) {
		e := v
		if e.Type != "message" || e.Message == nil {
			continue
		}
		acc += (len(e.Message.Text()) + 3) / 4
		if acc >= keep {
			for _, c := range cutPoints {
				if c >= i {
					cutIndex = c
					break
				}
			}
			break
		}
	}
	// Never walk the cut back past a message or compaction boundary.
	for cutIndex > 0 {
		prev := compactable[cutIndex-1]
		if prev.Type == "compaction" || prev.Type == "message" {
			break
		}
		cutIndex--
	}

	// Map the cut index back to a real entry id (virtual entries map into the
	// entries slice right after the previous compaction).
	var firstKept string
	switch {
	case cutIndex < len(virtual):
		firstKept = entries[prevCompIdx+1+cutIndex].ID
	case cutIndex >= len(virtual):
		firstKept = entries[prevCompIdx+1+cutIndex-len(virtual)].ID
	}

	// Split turn: cut lands inside a turn (cut entry is not a user message).
	cutEntry := compactable[cutIndex]
	isUserCut := cutEntry.Type == "message" && cutEntry.Message != nil && cutEntry.Message.Role == "user"
	historyEnd := cutIndex
	isSplitTurn := false
	if !isUserCut {
		for i := cutIndex; i >= 0; i-- {
			if e := compactable[i]; e.Type == "message" && e.Message != nil && e.Message.Role == "user" {
				historyEnd = i
				isSplitTurn = true
				break
			}
		}
	}

	var messagesToSummarize, turnPrefix, retainedTail []types.Message
	collect := func(from, to int) []types.Message {
		var out []types.Message
		for i := from; i < to; i++ {
			if e := compactable[i]; e.Type == "message" && e.Message != nil {
				out = append(out, *e.Message)
			}
		}
		return out
	}
	messagesToSummarize = collect(0, historyEnd)
	if isSplitTurn {
		turnPrefix = collect(historyEnd, cutIndex)
	}
	retainedTail = collect(cutIndex, len(compactable))

	if messagesToSummarize == nil && turnPrefix == nil {
		return nil, ErrNothingToCompact
	}
	return &Preparation{
		FirstKeptEntryID:    firstKept,
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefix,
		RetainedTail:        retainedTail,
		IsSplitTurn:         isSplitTurn,
		TokensBefore:        tokensBefore,
		PreviousSummary:     previousSummary,
	}, nil
}

// Execute generates the compaction summary for a Preparation (the only stage
// that calls the model). Returns the summary text and combined usage.
func Execute(ctx context.Context, prep *Preparation, sum Summarizer, cfg config.Compaction) (string, *types.Usage, error) {
	var summary string
	var usage *types.Usage
	if prep.IsSplitTurn && len(prep.TurnPrefixMessages) > 0 {
		historyText := "No prior history."
		if len(prep.MessagesToSummarize) > 0 {
			s, u, err := summarize(ctx, sum, cfg, prep.MessagesToSummarize, prep.PreviousSummary, false)
			if err != nil {
				return "", nil, err
			}
			historyText = s
			usage = u
		}
		ts, u, err := summarize(ctx, sum, cfg, prep.TurnPrefixMessages, "", true)
		if err != nil {
			return "", nil, err
		}
		summary = historyText + "\n\n---\n\n**Turn Context (split turn):**\n\n" + ts
		usage = combineUsage(usage, u)
	} else {
		s, u, err := summarize(ctx, sum, cfg, prep.MessagesToSummarize, prep.PreviousSummary, false)
		if err != nil {
			return "", nil, err
		}
		summary, usage = s, u
	}
	return summary, usage, nil
}

// Run is the one-step convenience entry (used by the /compact endpoint):
// Prepare + Execute + AppendCompaction. Returns ErrNothingToCompact when the
// session has nothing worth summarizing.
func Run(ctx context.Context, s *session.Session, sum Summarizer, cfg config.Compaction) (session.Entry, error) {
	prep, err := Prepare(s.LeafEntries(), cfg)
	if err != nil {
		return session.Entry{}, err
	}
	if prep == nil {
		return session.Entry{}, ErrNothingToCompact
	}
	summary, usage, err := Execute(ctx, prep, sum, cfg)
	if err != nil {
		return session.Entry{}, err
	}
	entry, err := s.AppendCompaction(summary, prep.FirstKeptEntryID, prep.TokensBefore, usage, prep.RetainedTail)
	if err != nil {
		return session.Entry{}, fmt.Errorf("append compaction: %w", err)
	}
	return entry, nil
}

// summarize builds the prompt (aligned with pi prompts) and calls the model.
// splitTurn selects the turn-prefix prompt; otherwise previousSummary selects
// the incremental UPDATE prompt.
func summarize(ctx context.Context, sum Summarizer, _ config.Compaction, msgs []types.Message, previousSummary string, splitTurn bool) (string, *types.Usage, error) {
	transcript := strings.Builder{}
	for _, m := range msgs {
		transcript.WriteString(m.Role + ": " + m.Text() + "\n")
	}
	t := transcript.String()
	if t == "" {
		t = "(empty)"
	}
	system := SystemPrompt
	user := "<conversation>\n" + t + "\n</conversation>\n\n"
	switch {
	case splitTurn:
		user += TurnPrefixSummarizationPrompt
	case previousSummary != "":
		system += "\n\n" + UpdateSystemPrompt
		user += "<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n" + UpdateSummarizationPrompt
	default:
		user += SummarizationPrompt
	}
	text, usage, err := sum.Summarize(ctx, system, user)
	if err != nil {
		return "", nil, fmt.Errorf("summarize context: %w", err)
	}
	return text, usage, nil
}

func combineUsage(a, b *types.Usage) *types.Usage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &types.Usage{
		Input:       a.Input + b.Input,
		Output:      a.Output + b.Output,
		CacheRead:   a.CacheRead + b.CacheRead,
		CacheWrite:  a.CacheWrite + b.CacheWrite,
		TotalTokens: a.TotalTokens + b.TotalTokens,
	}
}

func parseTime(ts string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// Static is a test summarizer.
type Static struct{ Text string }

// Summarize implements Summarizer.
func (s Static) Summarize(_ context.Context, _ string, user string) (string, *types.Usage, error) {
	t := s.Text
	if t == "" {
		t = "Summary of earlier conversation."
	}
	return t + "\n" + fmt.Sprintf("(source chars=%d)", len(user)), &types.Usage{Output: 20, TotalTokens: 20}, nil
}

// SystemPrompt is the base prompt for context summarization.
const SystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// UpdateSystemPrompt is appended to SystemPrompt for incremental summaries
// (pi UPDATE_SUMMARIZATION_PROMPT semantics).
const UpdateSystemPrompt = `
Update the existing summary with the NEW conversation messages provided in the <conversation> tags, preserving the <previous-summary> content. PRESERVE all existing information, ADD new progress, decisions, and context, and PRESERVE exact file paths, function names, and error messages.`

// SummarizationPrompt asks the model to summarize a conversation checkpoint.
const SummarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// UpdateSummarizationPrompt asks the model to update an existing summary.
const UpdateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// TurnPrefixSummarizationPrompt asks the model to summarize a retained turn prefix.
const TurnPrefixSummarizationPrompt = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the kept suffix]

Be concise. Focus on what's needed to understand the kept suffix.`

// StreamSummarizer asks a Streamer to summarize.
type StreamSummarizer struct {
	Stream func(ctx context.Context, system, user string) (string, *types.Usage, error)
}

// Summarize implements Summarizer.
func (s StreamSummarizer) Summarize(ctx context.Context, system, user string) (string, *types.Usage, error) {
	if s.Stream == nil {
		return Static{}.Summarize(ctx, system, user)
	}
	return s.Stream(ctx, system, user)
}
