package compact

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ki/internal/config"
	"ki/internal/session"
	"ki/internal/types"
)

type Summarizer interface {
	Summarize(ctx context.Context, system, user string) (string, *types.Usage, error)
}

var ErrNothingToCompact = errors.New("nothing to compact")

var ErrCompactionSkipped = errors.New("compaction skipped")

type Reason string

const (
	ReasonManual    Reason = "manual"
	ReasonPreflight Reason = "preflight"
	ReasonThreshold Reason = "threshold"
	ReasonOverflow  Reason = "overflow"
	ReasonServer    Reason = "server"
)

type Intent struct {
	Reason       Reason `json:"reason"`
	WillRetry    bool   `json:"willRetry"`
	Instructions string `json:"instructions,omitempty"`
}

func EstimateTokens(msgs []types.Message, recentCompaction int64) int {
	if tokens, ok := estimateFromUsage(msgs, recentCompaction); ok {
		return tokens
	}
	return char4(msgs)
}

func estimateFromUsage(msgs []types.Message, recentCompaction int64) (int, bool) {
	for i, v := range slices.Backward(msgs) {
		m := v
		if m.Role != "assistant" || m.Usage == nil {
			continue
		}
		if recentCompaction == 0 || m.Timestamp > recentCompaction {
			if t := m.Usage.TotalTokens; t > 0 {
				return t + trailingTokens(msgs[i+1:]), true
			}
			if t := m.Usage.Input + m.Usage.Output + m.Usage.CacheRead + m.Usage.CacheWrite; t > 0 {
				return t + trailingTokens(msgs[i+1:]), true
			}
		}
		break
	}
	return 0, false
}

func EstimateModelContext(ctx types.ModelContext, recentCompaction int64) int {
	if ctx.Portable {
		if raw, err := json.Marshal(ctx.Messages); err == nil {
			return (len(raw) + 3) / 4
		}
		return char4(ctx.Messages)
	}
	if ctx.Responses != nil {
		if tokens, ok := estimateFromUsage(ctx.Messages, recentCompaction); ok {
			return tokens
		}
	}
	tokens := EstimateTokens(ctx.Messages, recentCompaction)
	if ctx.Responses != nil {
		for _, item := range ctx.Responses.Items {
			tokens += (len(item) + 3) / 4
		}
	}
	return tokens
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

func ShouldRun(tokens, contextWindow int, cfg config.Compaction) bool {
	if !cfg.Enabled {
		return false
	}
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	if cfg.MaxContextTokens > 0 && cfg.MaxContextTokens < contextWindow {
		contextWindow = cfg.MaxContextTokens
	}
	return tokens > contextWindow-cfg.ReserveTokens
}

type Preparation struct {
	Intent              Intent
	SourceLeafID        string
	FirstKeptEntryID    string
	MessagesToSummarize []types.Message
	TurnPrefixMessages  []types.Message
	RetainedTail        []types.Message
	IsSplitTurn         bool
	TokensBefore        int
	PreviousSummary     string
}

type Result struct {
	Summary       string
	Usage         *types.Usage
	Details       any
	FromExtension bool
}

func Prepare(entries []session.Entry, cfg config.Compaction) (*Preparation, error) {
	return PrepareWithIntent(entries, cfg, Intent{})
}

func PrepareWithIntent(entries []session.Entry, cfg config.Compaction, intent Intent) (*Preparation, error) {
	keep := cfg.KeepRecentTokens
	if keep <= 0 {
		keep = 20000
	}
	prevCompIdx := -1
	for i, v := range slices.Backward(entries) {
		if v.Type == "compaction" && v.Responses == nil {
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
	portableExpanded := false
	for _, entry := range compactable {
		if entry.Type == "compaction" && entry.Responses != nil {
			portableExpanded = true
			break
		}
	}

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
	if portableExpanded {
		tokensBefore = EstimateModelContext(types.ModelContext{Messages: msgs, Portable: true}, lastCompactionAt)
	}

	var cutPoints []int
	for i, e := range compactable {
		if e.Type == "message" && e.Message != nil && e.Message.Role != "toolResult" {
			cutPoints = append(cutPoints, i)
		}
	}
	if len(cutPoints) == 0 {
		return nil, ErrNothingToCompact
	}

	acc := 0
	cutIndex := cutPoints[0]
	for i, v := range slices.Backward(compactable) {
		e := v
		if e.Type != "message" || e.Message == nil {
			continue
		}
		acc += (len(e.Message.Text()) + 3) / 4
		if acc >= keep {
			found := false
			for _, c := range cutPoints {
				if c >= i {
					cutIndex = c
					found = true
					break
				}
			}
			if !found {
				cutIndex = cutPoints[len(cutPoints)-1]
			}
			break
		}
	}
	for cutIndex > 0 {
		prev := compactable[cutIndex-1]
		if prev.Type == "compaction" || prev.Type == "message" {
			break
		}
		cutIndex--
	}

	var firstKept string
	switch {
	case cutIndex < len(virtual):
		firstKept = entries[prevCompIdx+1+cutIndex].ID
	case cutIndex >= len(virtual):
		firstKept = entries[prevCompIdx+1+cutIndex-len(virtual)].ID
	}

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
	sourceLeafID := ""
	if len(entries) > 0 {
		sourceLeafID = entries[len(entries)-1].ID
	}
	return &Preparation{
		Intent:              intent,
		SourceLeafID:        sourceLeafID,
		FirstKeptEntryID:    firstKept,
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefix,
		RetainedTail:        retainedTail,
		IsSplitTurn:         isSplitTurn,
		TokensBefore:        tokensBefore,
		PreviousSummary:     previousSummary,
	}, nil
}

func Generate(ctx context.Context, prep *Preparation, sum Summarizer, cfg config.Compaction) (Result, error) {
	return GenerateWithInputLimit(ctx, prep, sum, cfg, 0)
}

func GenerateWithInputLimit(ctx context.Context, prep *Preparation, sum Summarizer, cfg config.Compaction, maxInputTokens int) (Result, error) {
	summary, usage, err := ExecuteWithInputLimit(ctx, prep, sum, cfg, maxInputTokens)
	if err != nil {
		return Result{}, err
	}
	return Result{Summary: summary, Usage: usage}, nil
}

func Execute(ctx context.Context, prep *Preparation, sum Summarizer, cfg config.Compaction) (string, *types.Usage, error) {
	return ExecuteWithInputLimit(ctx, prep, sum, cfg, 0)
}

func ExecuteWithInputLimit(ctx context.Context, prep *Preparation, sum Summarizer, cfg config.Compaction, maxInputTokens int) (string, *types.Usage, error) {
	var summary string
	var usage *types.Usage
	if prep.IsSplitTurn && len(prep.TurnPrefixMessages) > 0 {
		historyText := "No prior history."
		if len(prep.MessagesToSummarize) > 0 {
			s, u, err := summarizeBounded(ctx, sum, cfg, prep.MessagesToSummarize, prep.PreviousSummary, false, prep.Intent.Instructions, maxInputTokens)
			if err != nil {
				return "", nil, err
			}
			historyText = s
			usage = u
		}
		ts, u, err := summarizeBounded(ctx, sum, cfg, prep.TurnPrefixMessages, "", true, prep.Intent.Instructions, maxInputTokens)
		if err != nil {
			return "", nil, err
		}
		summary = historyText + "\n\n---\n\n**Turn Context (split turn):**\n\n" + ts
		usage = combineUsage(usage, u)
	} else {
		s, u, err := summarizeBounded(ctx, sum, cfg, prep.MessagesToSummarize, prep.PreviousSummary, false, prep.Intent.Instructions, maxInputTokens)
		if err != nil {
			return "", nil, err
		}
		summary, usage = s, u
	}
	return summary, usage, nil
}

func summarizeBounded(ctx context.Context, sum Summarizer, cfg config.Compaction, msgs []types.Message, previousSummary string, splitTurn bool, instructions string, maxInputBytes int) (string, *types.Usage, error) {
	if maxInputBytes <= 0 {
		return summarize(ctx, sum, cfg, msgs, previousSummary, splitTurn, instructions)
	}
	system, user := summaryRequest(msgs, previousSummary, splitTurn, instructions)
	if len(system)+len(user) <= maxInputBytes {
		return callSummarizer(ctx, sum, system, user)
	}

	source := append([]types.Message{}, msgs...)
	if previousSummary != "" {
		source = append([]types.Message{{
			Role: "user", Content: []types.Content{{Type: "text", Text: "Previous conversation summary:\n" + previousSummary}},
		}}, source...)
	}
	emptySystem, emptyUser := summaryRequest(nil, "", splitTurn, instructions)
	payloadBytes := maxInputBytes - len(emptySystem) - len(emptyUser) - 1024
	if payloadBytes < 1024 {
		return "", nil, fmt.Errorf("summarization input budget %d is too small", maxInputBytes)
	}

	var totalUsage *types.Usage
	current := source
	for round := 0; round < 16; round++ {
		chunks := splitSummaryMessages(current, payloadBytes)
		if len(chunks) == 1 {
			text, chunkUsage, err := summarize(ctx, sum, cfg, chunks[0], "", splitTurn, instructions)
			if err != nil {
				return "", nil, err
			}
			totalUsage = combineUsage(totalUsage, chunkUsage)
			return text, totalUsage, nil
		}
		results, err := summarizeChunks(ctx, sum, chunks, splitTurn, instructions)
		if err != nil {
			return "", nil, err
		}
		partials := make([]types.Message, 0, len(results))
		for i, result := range results {
			totalUsage = combineUsage(totalUsage, result.usage)
			partials = append(partials, types.Message{
				Role: "user", Content: []types.Content{{
					Type: "text", Text: fmt.Sprintf("Partial checkpoint %d of %d:\n%s", i+1, len(results), result.text),
				}},
			})
		}
		current = partials
	}
	return "", nil, errors.New("hierarchical summarization did not converge")
}

const summaryChunkConcurrency = 4

type summaryChunkResult struct {
	text  string
	usage *types.Usage
	err   error
}

func summarizeChunks(ctx context.Context, sum Summarizer, chunks [][]types.Message, splitTurn bool, instructions string) ([]summaryChunkResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]summaryChunkResult, len(chunks))
	jobs := make(chan int, len(chunks))
	for i := range chunks {
		jobs <- i
	}
	close(jobs)

	workers := min(summaryChunkConcurrency, len(chunks))
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				system, user := summaryRequest(chunks[i], "", splitTurn, instructions)
				user += "\n\nThis is one segment of a larger transcript. Preserve decisions, code changes, failures, and pending work, but keep this partial checkpoint under 2,000 words."
				text, usage, err := callSummarizer(ctx, sum, system, user)
				results[i] = summaryChunkResult{text: text, usage: usage, err: err}
				if err != nil {
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, result := range results {
		if result.err != nil {
			return nil, result.err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func splitSummaryMessages(msgs []types.Message, maxBytes int) [][]types.Message {
	var chunks [][]types.Message
	var chunk []types.Message
	used := 0
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		chunks = append(chunks, chunk)
		chunk = nil
		used = 0
	}
	for _, message := range msgs {
		text := message.Text()
		if text == "" {
			continue
		}
		for len(text) > 0 {
			overhead := len(message.Role) + 3
			available := maxBytes - used - overhead
			if available <= 0 {
				flush()
				available = maxBytes - overhead
			}
			take := min(len(text), available)
			for take > 0 && take < len(text) && !utf8.RuneStart(text[take]) {
				take--
			}
			if take == 0 {
				_, size := utf8.DecodeRuneInString(text)
				take = size
			}
			part := types.Message{Role: message.Role, Content: []types.Content{{Type: "text", Text: text[:take]}}}
			chunk = append(chunk, part)
			used += overhead + take
			text = text[take:]
			if used >= maxBytes {
				flush()
			}
		}
	}
	flush()
	if len(chunks) == 0 {
		chunks = append(chunks, []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "(empty)"}}}})
	}
	return chunks
}

func Commit(s *session.Session, prep *Preparation, result Result) (session.Entry, error) {
	if prep == nil {
		return session.Entry{}, errors.New("missing compaction preparation")
	}
	if strings.TrimSpace(result.Summary) == "" {
		return session.Entry{}, errors.New("compaction summary is empty")
	}
	const maxCustomSummaryBytes = 8 << 20
	if len(result.Summary) > maxCustomSummaryBytes {
		return session.Entry{}, errors.New("compaction summary is too large")
	}
	if result.Details != nil {
		raw, err := json.Marshal(result.Details)
		if err != nil {
			return session.Entry{}, fmt.Errorf("marshal compaction details: %w", err)
		}
		const maxDetailsBytes = 1 << 20
		if len(raw) > maxDetailsBytes {
			return session.Entry{}, errors.New("compaction details are too large")
		}
	}
	if result.FromExtension {
		if err := validateCustomUsage(result.Usage); err != nil {
			return session.Entry{}, err
		}
	}
	entry, err := s.AppendPreparedCompaction(
		prep.SourceLeafID,
		result.Summary,
		prep.FirstKeptEntryID,
		prep.TokensBefore,
		result.Usage,
		prep.RetainedTail,
		result.Details,
	)
	if err != nil {
		return session.Entry{}, fmt.Errorf("append compaction: %w", err)
	}
	return entry, nil
}

func validateCustomUsage(usage *types.Usage) error {
	if usage == nil {
		return nil
	}
	if usage.Input < 0 || usage.Output < 0 || usage.CacheRead < 0 ||
		usage.CacheWrite < 0 || usage.TotalTokens < 0 {
		return errors.New("custom compaction usage contains negative tokens")
	}
	sum := usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
	if usage.TotalTokens != 0 && usage.TotalTokens != sum {
		return errors.New("custom compaction usage total does not match token fields")
	}
	if usage.Cost == nil {
		return nil
	}
	cost := usage.Cost
	values := []float64{cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite, cost.Total}
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("custom compaction usage contains invalid cost")
		}
	}
	sumCost := cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	tolerance := math.Max(1e-9, math.Abs(sumCost)*1e-9)
	if math.Abs(cost.Total-sumCost) > tolerance {
		return errors.New("custom compaction usage total does not match cost fields")
	}
	return nil
}

func summarize(ctx context.Context, sum Summarizer, _ config.Compaction, msgs []types.Message, previousSummary string, splitTurn bool, instructions string) (string, *types.Usage, error) {
	system, user := summaryRequest(msgs, previousSummary, splitTurn, instructions)
	return callSummarizer(ctx, sum, system, user)
}

func summaryRequest(msgs []types.Message, previousSummary string, splitTurn bool, instructions string) (string, string) {
	transcript := strings.Builder{}
	for _, m := range msgs {
		transcript.WriteString(m.Role)
		transcript.WriteString(": ")
		transcript.WriteString(m.Text())
		transcript.WriteByte('\n')
	}
	t := cmp.Or(transcript.String(), "(empty)")
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
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		user += "\n\n<custom-instructions>\n" + instructions + "\n</custom-instructions>\n"
	}
	return system, user
}

func callSummarizer(ctx context.Context, sum Summarizer, system, user string) (string, *types.Usage, error) {
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

type Static struct{ Text string }

func (s Static) Summarize(_ context.Context, _ string, user string) (string, *types.Usage, error) {
	t := cmp.Or(s.Text, "Summary of earlier conversation.")
	return t + "\n" + fmt.Sprintf("(source chars=%d)", len(user)), &types.Usage{Output: 20, TotalTokens: 20}, nil
}

const SystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const UpdateSystemPrompt = `
Update the existing summary with the NEW conversation messages provided in the <conversation> tags, preserving the <previous-summary> content. PRESERVE all existing information, ADD new progress, decisions, and context, and PRESERVE exact file paths, function names, and error messages.`

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

const TurnPrefixSummarizationPrompt = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the kept suffix]

Be concise. Focus on what's needed to understand the kept suffix.`

type StreamSummarizer struct {
	Stream func(ctx context.Context, system, user string) (string, *types.Usage, error)
}

func (s StreamSummarizer) Summarize(ctx context.Context, system, user string) (string, *types.Usage, error) {
	if s.Stream == nil {
		return Static{}.Summarize(ctx, system, user)
	}
	return s.Stream(ctx, system, user)
}
