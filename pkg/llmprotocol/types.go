package llmprotocol

import (
	"context"
	"net/http"
	"strings"
)

// Supported wire protocol names accepted by Client.API.
const (
	APICompletions = "completions"
	APIResponses   = "responses"
	APIAnthropic   = "anthropic"
)

// Content is one block in a protocol-neutral message.
type Content struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Data      string         `json:"data,omitempty"`
	MIMEType  string         `json:"mimeType,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	ToolType  string         `json:"toolType,omitempty"`
	Input     string         `json:"input,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	// ItemID is a provider item identifier, for example an OpenAI Responses
	// output item id that must be retained when an assistant turn is replayed.
	ItemID string `json:"itemId,omitempty"`
	// ArgumentsRaw retains valid streamed function arguments or opaque custom
	// tool input for providers that need the original wire representation.
	ArgumentsRaw string `json:"argumentsRaw,omitempty"`
	// ThinkingSignature and ThinkingData are opaque provider-owned reasoning
	// state. The protocol package does not interpret their contents.
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	ThinkingData      string `json:"thinkingData,omitempty"`
	TextSignature     string `json:"textSignature,omitempty"`
	// StreamIndex is transient parser state and is never serialized.
	StreamIndex int `json:"-"`
}

// Usage is provider token accounting. Cost is not calculated by this package.
type Usage struct {
	Input       int        `json:"input"`
	Output      int        `json:"output"`
	CacheRead   int        `json:"cacheRead"`
	CacheWrite  int        `json:"cacheWrite"`
	TotalTokens int        `json:"totalTokens"`
	Cost        *UsageCost `json:"cost,omitempty"`
}

// UsageCost is optional caller-owned pricing information.
type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// Message is a protocol-neutral user, assistant, or tool-result message.
type Message struct {
	Role         string    `json:"role"`
	Content      []Content `json:"content"`
	ResponseID   string    `json:"responseId,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
	StopReason   string    `json:"stopReason,omitempty"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	ToolCallID   string    `json:"toolCallId,omitempty"`
	ToolName     string    `json:"toolName,omitempty"`
	ToolType     string    `json:"toolType,omitempty"`
	IsError      bool      `json:"isError,omitempty"`
}

// Text returns concatenated text blocks.
func (m Message) Text() string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// ToolCalls returns tool-call content blocks.
func (m Message) ToolCalls() []Content {
	var out []Content
	for _, c := range m.Content {
		if c.Type == "toolCall" {
			out = append(out, c)
		}
	}
	return out
}

// AssistantDelta is one incremental assistant update.
type AssistantDelta struct {
	Type       string  `json:"type"`
	Delta      string  `json:"delta,omitempty"`
	ToolCallID string  `json:"toolCallId,omitempty"`
	ToolName   string  `json:"toolName,omitempty"`
	Partial    Message `json:"partial"`
}

// ToolSpec describes a function or custom tool exposed to a provider.
type ToolSpec struct {
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Format      *ToolFormat    `json:"format,omitempty"`
}

// ToolFormat describes the grammar accepted by a Responses custom tool.
type ToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

// Request is one provider call. Provider-specific request options are kept
// explicit so callers can use the same neutral IR with all three protocols.
type Request struct {
	System                  string             `json:"system"`
	Messages                []Message          `json:"messages"`
	Tools                   []ToolSpec         `json:"tools"`
	Provider                string             `json:"provider"`
	Model                   string             `json:"model"`
	MaxTokens               int                `json:"maxTokens,omitempty"`
	ThinkingEffort          string             `json:"thinkingEffort,omitempty"`
	ThinkingFormat          string             `json:"thinkingFormat,omitempty"`
	MaxTokensField          string             `json:"maxTokensField,omitempty"`
	SupportsReasoningEffort bool               `json:"supportsReasoningEffort,omitempty"`
	ForceAdaptiveThinking   bool               `json:"forceAdaptiveThinking,omitempty"`
	ThinkingLevelMap        map[string]*string `json:"thinkingLevelMap,omitempty"`
}

// HTTPDoer is the transport injected into a Client. *http.Client satisfies it.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client streams one of the supported provider protocols.
type Client struct {
	Doer   HTTPDoer
	APIKey string
	Base   string
	API    string // APICompletions | APIResponses | APIAnthropic
}

// NewClient builds a protocol client. A nil doer uses http.DefaultClient.
func NewClient(api, base, key string, doer HTTPDoer) *Client {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Client{Doer: doer, APIKey: key, Base: strings.TrimRight(base, "/"), API: api}
}

// Stream produces an assistant message and incremental deltas.
type Streamer interface {
	Stream(context.Context, Request, func(AssistantDelta) error) (Message, error)
}
