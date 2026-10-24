package types

import (
	"encoding/json"
	"strings"
)

type ProviderBinding struct {
	Provider   string `json:"provider"`
	API        string `json:"api"`
	BaseURL    string `json:"baseUrl"`
	Model      string `json:"model"`
	Credential string `json:"credential"`
	Compaction string `json:"compaction"`
}

func (b ProviderBinding) Equal(other ProviderBinding) bool {
	return b.SameScope(other) &&
		compactionProtocol(b.Compaction) == compactionProtocol(other.Compaction)
}

func (b ProviderBinding) SameScope(other ProviderBinding) bool {
	return b.Provider == other.Provider &&
		b.API == other.API &&
		strings.TrimRight(b.BaseURL, "/") == strings.TrimRight(other.BaseURL, "/") &&
		b.Model == other.Model &&
		b.Credential == other.Credential
}

func compactionProtocol(protocol string) string {
	if protocol == "" {
		return "openai"
	}
	return protocol
}

type ResponsesContext struct {
	Binding ProviderBinding   `json:"binding"`
	Items   []json.RawMessage `json:"items"`
}

type ModelContext struct {
	Responses *ResponsesContext `json:"responses,omitempty"`
	Messages  []Message         `json:"messages,omitempty"`
	Portable  bool              `json:"-"`
}

type Content struct {
	Type              string         `json:"type"`
	Text              string         `json:"text,omitempty"`
	Data              string         `json:"data,omitempty"`
	MIMEType          string         `json:"mimeType,omitempty"`
	Path              string         `json:"path,omitempty"`
	Size              int64          `json:"size,omitzero"`
	Thinking          string         `json:"thinking,omitempty"`
	ID                string         `json:"id,omitempty"`
	Name              string         `json:"name,omitempty"`
	ToolType          string         `json:"toolType,omitempty"`
	Input             string         `json:"input,omitempty"`
	Arguments         map[string]any `json:"arguments,omitempty"`
	ItemID            string         `json:"itemId,omitempty"`
	ArgumentsRaw      string         `json:"argumentsRaw,omitempty"`
	ThinkingSignature string         `json:"thinkingSignature,omitempty"`
	ThinkingData      string         `json:"thinkingData,omitempty"`
	TextSignature     string         `json:"textSignature,omitempty"`
	StreamIndex       int            `json:"-"`
}

type Usage struct {
	Input       int        `json:"input"`
	Output      int        `json:"output"`
	CacheRead   int        `json:"cacheRead"`
	CacheWrite  int        `json:"cacheWrite"`
	TotalTokens int        `json:"totalTokens"`
	Cost        *UsageCost `json:"cost,omitempty"`
}

type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type CacheMiss struct {
	MissedTokens int     `json:"missedTokens"`
	MissRatio    float64 `json:"missRatio"`
}

type CompletionIdentity struct {
	TaskID     string `json:"taskId"`
	Generation uint64 `json:"generation"`
}

type Message struct {
	ContextOnly     bool                `json:"contextOnly,omitempty"`
	ClientRequestID string              `json:"clientRequestId,omitempty"`
	Completion      *CompletionIdentity `json:"completion,omitempty"`
	Role            string              `json:"role"`
	Content         []Content           `json:"content"`
	Timestamp       int64               `json:"timestamp,omitzero"`
	API             string              `json:"api,omitempty"`
	Provider        string              `json:"provider,omitempty"`
	Model           string              `json:"model,omitempty"`
	ResponseID      string              `json:"responseId,omitempty"`
	ResponsesItems  []json.RawMessage   `json:"-"`
	Usage           *Usage              `json:"usage,omitempty"`
	StopReason      string              `json:"stopReason,omitempty"`
	ErrorMessage    string              `json:"errorMessage,omitempty"`
	CancelReason    string              `json:"cancelReason,omitempty"`
	CancelSource    string              `json:"cancelSource,omitempty"`
	ToolCallID      string              `json:"toolCallId,omitempty"`
	ToolName        string              `json:"toolName,omitempty"`
	Details         any                 `json:"details,omitempty"`
	ToolType        string              `json:"toolType,omitempty"`
	Origin          string              `json:"origin,omitempty"`
	External        map[string]string   `json:"external,omitempty"`
	IsError         bool                `json:"isError,omitzero"`
	LatencyMs       int64               `json:"latencyMs,omitzero"`
	TTFTMs          int64               `json:"ttftMs,omitzero"`
	DurationMs      int64               `json:"durationMs"`
}

func (m Message) Text() string {
	var s strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "" {
			s.WriteString(c.Text)
		}
	}
	return s.String()
}

func (m Message) ToolCalls() []Content {
	var out []Content
	for _, c := range m.Content {
		if c.Type == "toolCall" {
			out = append(out, c)
		}
	}
	return out
}
