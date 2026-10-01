package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"

	"ki/internal/types"
)

// ContextEstimate is a view-only UTF-8/4 estimate of retained textual content.
// A nil field is unknown/not applicable; a present zero is a known empty block.
// Provider usage, encoding transformations and opaque content are not estimated.
type ContextEstimate struct {
	System  *int `json:"system,omitempty"`
	Tools   *int `json:"tools,omitempty"`
	Message *int `json:"message,omitempty"`
	Summary *int `json:"summary,omitempty"`
}

func contextTokens(text string) int { return (len(text) + 3) / 4 }
func contextNumber(n int) *int      { return &n }

func toolCallTokens(name, input, raw string, arguments any) int {
	if input == "" {
		switch {
		case arguments != nil:
			body, err := json.MarshalIndent(arguments, "", "  ")
			if err == nil {
				input = string(body)
			}
		case raw != "":
			var body bytes.Buffer
			if json.Indent(&body, []byte(raw), "", "  ") == nil {
				input = body.String()
			} else {
				input = raw
			}
		default:
			input = "{}"
		}
	}
	return contextTokens(name + "\n" + input)
}

func messageContextTokens(message *types.Message) int {
	total := 0
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			total += contextTokens(block.Text)
		case "thinking":
			total += contextTokens(block.Thinking)
		case "toolCall":
			var arguments any
			if block.Arguments != nil {
				arguments = block.Arguments
			}
			total += toolCallTokens(block.Name, block.Input, block.ArgumentsRaw, arguments)
		}
	}
	return total
}

func estimateContext(e Entry) *ContextEstimate {
	if e.Truncated || e.PromptUnchanged {
		// Public shortened bodies cannot recover their original size. Preserve
		// an earlier estimate rather than pricing a preview as complete content.
		return e.ContextEstimate
	}
	switch e.Type {
	case "request_header":
		tools := 0
		for _, schema := range e.Tools {
			if raw, err := json.Marshal(schema); err == nil {
				tools += (len(raw) + 3) / 4
			}
		}
		return &ContextEstimate{System: contextNumber(contextTokens(e.System)), Tools: contextNumber(tools)}
	case "message":
		if e.Message != nil {
			return &ContextEstimate{Message: contextNumber(messageContextTokens(e.Message))}
		}
	case "compaction":
		if !e.RemoteContext && e.Responses == nil {
			return &ContextEstimate{Summary: contextNumber(contextTokens("Previous conversation summary:\n" + e.Summary))}
		}
	}
	return nil
}

func withContextEstimate(e Entry) Entry {
	if e.ContextEstimate == nil {
		e.ContextEstimate = estimateContext(e)
	}
	return e
}

// Retain only small digests/counts, never decoded tool bodies, during a scan.
type contextEstimateCache map[[32]byte]int

func (cache contextEstimateCache) tools(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, nil
	}
	key := sha256.Sum256(raw)
	if value, ok := cache[key]; ok {
		return value, nil
	}
	var schemas []json.RawMessage
	if err := json.Unmarshal(raw, &schemas); err != nil {
		return 0, err
	}
	total := 0
	for _, schema := range schemas {
		var compact bytes.Buffer
		if err := json.Compact(&compact, schema); err != nil {
			return 0, err
		}
		total += (compact.Len() + 3) / 4
	}
	if len(cache) < 64 {
		cache[key] = total
	}
	return total, nil
}
