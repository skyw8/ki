package types

import "strings"

// Content is one block inside a message.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Path     string `json:"path,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	// ToolType and Input are persisted: Responses rejects a resumed custom
	// call if it is replayed as a function call or loses its raw input.
	ToolType     string         `json:"toolType,omitempty"`
	Input        string         `json:"input,omitempty"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	ItemID       string         `json:"-"`
	ArgumentsRaw string         `json:"-"`
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

// Message is a conversation item (user / assistant / toolResult).
type Message struct {
	Role         string    `json:"role"`
	Content      []Content `json:"content"`
	Timestamp    int64     `json:"timestamp,omitempty"`
	API          string    `json:"api,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
	StopReason   string    `json:"stopReason,omitempty"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	ToolCallID   string    `json:"toolCallId,omitempty"`
	ToolName     string    `json:"toolName,omitempty"`
	// Details is persisted for clients and diagnostics but provider adapters
	// deliberately omit it from model requests.
	Details any `json:"details,omitempty"`
	// ToolType selects the matching function/custom output wire item.
	ToolType   string `json:"toolType,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
	LatencyMs  int64  `json:"latencyMs,omitempty"`
	TTFTMs     int64  `json:"ttftMs,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
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
