package types

import (
	"encoding/json"
	"strings"
)

// ProviderBinding identifies the provider endpoint and model that owns opaque
// context. Every field participates in compatibility checks: provider-owned
// encrypted state must never cross an endpoint or model switch.
type ProviderBinding struct {
	Provider   string `json:"provider"`
	API        string `json:"api"`
	BaseURL    string `json:"baseUrl"`
	Model      string `json:"model"`
	Credential string `json:"credential"`
	Compaction string `json:"compaction"`
}

// Equal reports whether opaque provider context can be replayed to other.
func (b ProviderBinding) Equal(other ProviderBinding) bool {
	return b.SameScope(other) &&
		compactionProtocol(b.Compaction) == compactionProtocol(other.Compaction)
}

// SameScope reports whether two bindings identify the same provider request
// identity without requiring the checkpoint-producing compaction protocol.
func (b ProviderBinding) SameScope(other ProviderBinding) bool {
	return b.Provider == other.Provider &&
		b.API == other.API &&
		strings.TrimRight(b.BaseURL, "/") == strings.TrimRight(other.BaseURL, "/") &&
		b.Model == other.Model &&
		b.Credential == other.Credential
}

func compactionProtocol(protocol string) string {
	if protocol == "" {
		// Why: checkpoints written before the capability split had no protocol
		// field and could only have come from OpenAI compaction. Treating them
		// as OpenAI preserves old JSONL without making them compatible with the
		// later Codex V2 standalone protocol.
		return "openai"
	}
	return protocol
}

// ResponsesContext is an opaque canonical Responses input prefix. Items are
// the complete ordered output returned by /responses/compact; Ki does not
// interpret or prune them.
type ResponsesContext struct {
	Binding ProviderBinding   `json:"binding"`
	Items   []json.RawMessage `json:"items"`
}

// ModelContext separates portable messages from an optional provider-owned
// prefix. Non-Responses protocols use Messages and ignore Responses.
type ModelContext struct {
	Responses *ResponsesContext `json:"responses,omitempty"`
	Messages  []Message         `json:"messages,omitempty"`
	// Portable marks an expanded fallback from an incompatible opaque
	// checkpoint. Provider usage after that checkpoint describes the compact
	// prefix, not this larger durable transcript.
	Portable bool `json:"-"`
}

// Content is one block inside a message.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Path     string `json:"path,omitempty"`
	Size     int64  `json:"size,omitzero"`
	Thinking string `json:"thinking,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	// ToolType and Input are persisted: Responses rejects a resumed custom
	// call if it is replayed as a function call or loses its raw input.
	ToolType  string         `json:"toolType,omitempty"`
	Input     string         `json:"input,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	// ItemID is the provider response item identifier. Responses providers
	// need it when an assistant turn is replayed as a later input item.
	ItemID string `json:"itemId,omitempty"`
	// ArgumentsRaw preserves a provider's exact streamed argument text. It is
	// useful for custom dialects that distinguish an empty object from raw
	// input which could not be decoded as a JSON object.
	ArgumentsRaw string `json:"argumentsRaw,omitempty"`
	// ThinkingSignature is opaque provider-owned reasoning state (for example
	// Codex encrypted reasoning content). Ki never interprets it.
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	// ThinkingData is opaque redacted-thinking content returned by Anthropic.
	// It is round-tripped without exposing the provider-owned payload to the UI.
	ThinkingData string `json:"thinkingData,omitempty"`
	// TextSignature is opaque provider-owned output-text state, when a provider
	// needs it to replay an assistant item without reconstructing it.
	TextSignature string `json:"textSignature,omitempty"`
	// StreamIndex is the provider stream content-block index used while an
	// Anthropic SSE response is being accumulated. It must not be persisted.
	StreamIndex int `json:"-"`
}

// Usage is provider token accounting.
type Usage struct {
	Input       int        `json:"input"`
	Output      int        `json:"output"`
	CacheRead   int        `json:"cacheRead"`
	CacheWrite  int        `json:"cacheWrite"`
	TotalTokens int        `json:"totalTokens"`
	Cost        *UsageCost `json:"cost,omitempty"`
}

// UsageCost contains token prices and the resulting request cost in USD.
type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// CompletionIdentity names one result, not the resumable logical task.
type CompletionIdentity struct {
	TaskID     string `json:"taskId"`
	Generation uint64 `json:"generation"`
}

// Message is a conversation item (user / assistant / toolResult).
type Message struct {
	// ClientRequestID correlates accepted input with its persisted entry. It is
	// transport metadata, never provider prompt content or a text dedupe key.
	ClientRequestID string              `json:"clientRequestId,omitempty"`
	Completion      *CompletionIdentity `json:"completion,omitempty"`
	Role            string              `json:"role"`
	Content         []Content           `json:"content"`
	Timestamp       int64               `json:"timestamp,omitzero"`
	API             string              `json:"api,omitempty"`
	Provider        string              `json:"provider,omitempty"`
	Model           string              `json:"model,omitempty"`
	// ResponseID is the provider response identifier used by resumable
	// Responses-style runtimes. It is deliberately optional for other APIs.
	ResponseID string `json:"responseId,omitempty"`
	// ResponsesItems is the transient canonical provider output suffix from a
	// server-side compaction. The emitter promotes it to a dedicated session
	// checkpoint before persisting or publishing the message, so opaque
	// encrypted state never leaks through ordinary message JSON.
	ResponsesItems []json.RawMessage `json:"-"`
	Usage          *Usage            `json:"usage,omitempty"`
	StopReason     string            `json:"stopReason,omitempty"`
	ErrorMessage   string            `json:"errorMessage,omitempty"`
	// CancelReason and CancelSource preserve why an aborted run stopped.
	// context.Canceled alone cannot distinguish an explicit user stop from
	// shutdown, deletion, extension control, or an unknown parent cancellation.
	CancelReason string `json:"cancelReason,omitempty"`
	CancelSource string `json:"cancelSource,omitempty"`
	ToolCallID   string `json:"toolCallId,omitempty"`
	ToolName     string `json:"toolName,omitempty"`
	// Details is persisted for clients and diagnostics but provider adapters
	// deliberately omit it from model requests.
	Details any `json:"details,omitempty"`
	// ToolType selects the matching function/custom output wire item.
	ToolType string `json:"toolType,omitempty"`
	// Origin marks non-human user turns (for example extension:<name> or agent:<task-id>). Empty is the human user.
	Origin     string            `json:"origin,omitempty"`
	External   map[string]string `json:"external,omitempty"`
	IsError    bool              `json:"isError,omitzero"`
	LatencyMs  int64             `json:"latencyMs,omitzero"`
	TTFTMs     int64             `json:"ttftMs,omitzero"`
	DurationMs int64             `json:"durationMs"` // not omitempty: a fast tool reports a real 0ms
}

// Text returns concatenated text blocks.
func (m Message) Text() string {
	var s strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "" {
			s.WriteString(c.Text)
		}
	}
	return s.String()
}

// ToolCalls returns toolCall content blocks.
func (m Message) ToolCalls() []Content {
	var out []Content
	for _, c := range m.Content {
		if c.Type == "toolCall" {
			out = append(out, c)
		}
	}
	return out
}
