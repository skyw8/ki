package llmprotocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	APICompletions = "completions"
	APIResponses   = "responses"
	APIAnthropic   = "anthropic"
)

type Content struct {
	Type              string         `json:"type"`
	Text              string         `json:"text,omitempty"`
	Data              string         `json:"data,omitempty"`
	MIMEType          string         `json:"mimeType,omitempty"`
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

type ResponsesItem []byte

func (i ResponsesItem) MarshalJSON() ([]byte, error) {
	if err := validateResponsesItem(i); err != nil {
		return nil, err
	}
	return i, nil
}

func (i *ResponsesItem) UnmarshalJSON(data []byte) error {
	if i == nil {
		return fmt.Errorf("cannot unmarshal Responses item into nil receiver")
	}
	if err := validateResponsesItem(data); err != nil {
		return err
	}
	*i = append((*i)[:0], data...)
	return nil
}

func (i ResponsesItem) RawJSON() []byte {
	return append([]byte(nil), i...)
}

func (i ResponsesItem) Type() string {
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(i, &header) != nil {
		return ""
	}
	return header.Type
}

func validateResponsesItem(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid Responses item JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return fmt.Errorf("Responses item must be a JSON object")
	}
	return nil
}

type ResponsesWindow []ResponsesItem

type Message struct {
	Role           string          `json:"role"`
	Content        []Content       `json:"content"`
	ResponseID     string          `json:"responseId,omitempty"`
	Usage          *Usage          `json:"usage,omitempty"`
	StopReason     string          `json:"stopReason,omitempty"`
	ErrorMessage   string          `json:"errorMessage,omitempty"`
	ToolCallID     string          `json:"toolCallId,omitempty"`
	ToolName       string          `json:"toolName,omitempty"`
	ToolType       string          `json:"toolType,omitempty"`
	IsError        bool            `json:"isError,omitzero"`
	ResponsesItems ResponsesWindow `json:"responsesItems,omitempty"`
}

func (m Message) Text() string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
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

type AssistantDelta struct {
	Type       string  `json:"type"`
	Delta      string  `json:"delta,omitempty"`
	ToolCallID string  `json:"toolCallId,omitempty"`
	ToolName   string  `json:"toolName,omitempty"`
	Partial    Message `json:"partial"`
}

type ToolSpec struct {
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Format      *ToolFormat    `json:"format,omitempty"`
}

type ToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax"`
	Definition string `json:"definition"`
}

type Request struct {
	System                    string             `json:"system"`
	Messages                  []Message          `json:"messages"`
	Tools                     []ToolSpec         `json:"tools"`
	Provider                  string             `json:"provider"`
	Model                     string             `json:"model"`
	MaxTokens                 int                `json:"maxTokens,omitzero"`
	ThinkingEffort            string             `json:"thinkingEffort,omitempty"`
	ThinkingFormat            string             `json:"thinkingFormat,omitempty"`
	MaxTokensField            string             `json:"maxTokensField,omitempty"`
	SupportsReasoningEffort   bool               `json:"supportsReasoningEffort,omitzero"`
	ForceAdaptiveThinking     bool               `json:"forceAdaptiveThinking,omitzero"`
	ThinkingLevelMap          map[string]*string `json:"thinkingLevelMap,omitempty"`
	ResponsesWindow           ResponsesWindow    `json:"responsesWindow,omitempty"`
	ResponsesCompactThreshold int                `json:"responsesCompactThreshold,omitzero"`
}

type ResponsesCompactRequest struct {
	Model        string
	Instructions string
	Window       ResponsesWindow
	Messages     []Message
}

type ResponsesCompactResult struct {
	ID     string
	Output ResponsesWindow
	Usage  *Usage
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	Doer        HTTPDoer
	APIKey      string
	Base        string
	API         string
	IdleTimeout time.Duration
}

func NewClient(api, base, key string, doer HTTPDoer) *Client {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Client{Doer: doer, APIKey: key, Base: strings.TrimRight(base, "/"), API: api, IdleTimeout: 5 * time.Minute}
}

type Streamer interface {
	Stream(context.Context, Request, func(AssistantDelta) error) (Message, error)
}

type ResponsesCompactor interface {
	CompactResponses(context.Context, ResponsesCompactRequest) (ResponsesCompactResult, error)
}
