package compact

import (
	"context"
	"fmt"
	"strings"

	"ki/internal/config"
	"ki/internal/session"
	"ki/internal/types"
)

// Summarizer produces a structured summary of older messages.
type Summarizer interface {
	Summarize(ctx context.Context, transcript string) (string, *types.Usage, error)
}

// EstimateTokens is a coarse char/4 estimate.
func EstimateTokens(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		n += (len(m.Text()) + 3) / 4
		if m.Usage != nil && m.Usage.TotalTokens > n {
			// prefer last assistant total as context size when present
		}
	}
	if len(msgs) > 0 {
		if a := msgs[len(msgs)-1]; a.Role == "assistant" && a.Usage != nil && a.Usage.TotalTokens > 0 {
			return a.Usage.TotalTokens
		}
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
	return tokens > contextWindow-cfg.ReserveTokens
}

// Run summarizes older entries, appends a compaction record, and returns the new system-facing history.
func Run(ctx context.Context, s *session.Session, sum Summarizer, cfg config.Compaction) (session.Entry, error) {
	entries := s.Entries()
	msgs := s.MessagesToLeaf()
	tokensBefore := EstimateTokens(msgs)
	keep := cfg.KeepRecentTokens
	if keep <= 0 {
		keep = 20000
	}
	// Walk newest→oldest until we have ~keep tokens; cut there.
	acc := 0
	cut := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type != "message" || entries[i].Message == nil {
			continue
		}
		acc += (len(entries[i].Message.Text()) + 3) / 4
		if acc >= keep {
			cut = i
			break
		}
	}
	firstKept := ""
	if cut < len(entries) {
		firstKept = entries[cut].ID
	}
	var old []string
	for i := 0; i < cut; i++ {
		if entries[i].Type == "message" && entries[i].Message != nil {
			old = append(old, entries[i].Message.Role+": "+entries[i].Message.Text())
		}
	}
	transcript := strings.Join(old, "\n")
	if transcript == "" {
		transcript = "(empty)"
	}
	summary, usage, err := sum.Summarize(ctx, transcript)
	if err != nil {
		return session.Entry{}, err
	}
	return s.AppendCompaction(summary, firstKept, tokensBefore, usage)
}

// Static is a test summarizer.
type Static struct{ Text string }

// Summarize implements Summarizer.
func (s Static) Summarize(ctx context.Context, transcript string) (string, *types.Usage, error) {
	t := s.Text
	if t == "" {
		t = "Summary of earlier conversation."
	}
	return t + "\n" + fmt.Sprintf("(source chars=%d)", len(transcript)), &types.Usage{Output: 20, TotalTokens: 20}, nil
}

const SystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// StreamSummarizer asks a Streamer to summarize.
type StreamSummarizer struct {
	Stream func(ctx context.Context, system, user string) (string, *types.Usage, error)
}

// Summarize implements Summarizer.
func (s StreamSummarizer) Summarize(ctx context.Context, transcript string) (string, *types.Usage, error) {
	if s.Stream == nil {
		return Static{}.Summarize(ctx, transcript)
	}
	return s.Stream(ctx, SystemPrompt, "Summarize the following conversation:\n\n"+transcript)
}
