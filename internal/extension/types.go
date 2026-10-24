package extension

import (
	"context"
	"encoding/json"
	"net/http"

	"ki/internal/loop"
	"ki/internal/provider"
	toolapi "ki/internal/tool"
	"ki/internal/types"
)

type Event struct {
	Type             string            `json:"type"`
	SessionID        string            `json:"sessionId,omitempty"`
	EntryID          string            `json:"entryId,omitempty"`
	Role             string            `json:"role,omitempty"`
	Timestamp        int64             `json:"timestamp,omitzero"`
	ToolCallID       string            `json:"toolCallId,omitempty"`
	ToolName         string            `json:"toolName,omitempty"`
	IsError          bool              `json:"isError,omitzero"`
	DurationMs       int64             `json:"durationMs"`
	Reason           string            `json:"reason,omitempty"`
	OK               bool              `json:"ok,omitzero"`
	WillRetry        bool              `json:"willRetry,omitzero"`
	Strategy         string            `json:"strategy,omitempty"`
	Status           string            `json:"status,omitempty"`
	FromExtension    bool              `json:"fromExtension,omitzero"`
	FirstKeptEntryID string            `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int               `json:"tokensBefore,omitzero"`
	Usage            *types.Usage      `json:"usage,omitempty"`
	Provider         string            `json:"provider,omitempty"`
	Model            string            `json:"model,omitempty"`
	RunID            string            `json:"runId,omitempty"`
	Text             string            `json:"text,omitempty"`
	StopReason       string            `json:"stopReason,omitempty"`
	ErrorMessage     string            `json:"errorMessage,omitempty"`
	ToolTitle        string            `json:"toolTitle,omitempty"`
	External         map[string]string `json:"external,omitempty"`
}

type Registration struct {
	Tools         []ToolSpec                       `json:"tools"`
	Commands      []CommandSpec                    `json:"commands"`
	Providers     []provider.ExtensionProviderSpec `json:"providers"`
	Fallback      bool                             `json:"fallback"`
	Subscriptions []Subscription                   `json:"subscriptions"`
	syncEvents    map[string]bool
	asyncEvents   map[string]bool
}

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Snippet     string         `json:"snippet,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	TimeoutMs   int            `json:"timeoutMs,omitzero"`
}

type CommandSpec struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	ArgumentHint string   `json:"argumentHint,omitempty"`
	Completions  []string `json:"completions,omitempty"`
}

type CatalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Aliases     []string `json:"-"`
}

type SessionContribution struct {
	Tools    []CatalogEntry
	Commands []CatalogEntry
}

type ToolCall struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Args     map[string]any `json:"args"`
	ToolType string         `json:"toolType,omitempty"`
}

type Block struct {
	Reason    string `json:"reason"`
	Terminate bool   `json:"terminate,omitzero"`
}

type ResultPatch struct {
	Content   []types.Content `json:"content,omitempty"`
	Details   any             `json:"details,omitempty"`
	IsError   *bool           `json:"isError,omitempty"`
	Terminate *bool           `json:"terminate,omitempty"`
}

type CompactPreparation struct {
	Strategy            string          `json:"strategy"`
	SourceLeafID        string          `json:"sourceLeafId,omitempty"`
	FirstKeptEntryID    string          `json:"firstKeptEntryId,omitempty"`
	MessagesToSummarize []types.Message `json:"messagesToSummarize,omitempty"`
	TurnPrefixMessages  []types.Message `json:"turnPrefixMessages,omitempty"`
	RetainedTail        []types.Message `json:"retainedTail,omitempty"`
	IsSplitTurn         bool            `json:"isSplitTurn,omitzero"`
	TokensBefore        int             `json:"tokensBefore,omitzero"`
	PreviousSummary     string          `json:"previousSummary,omitempty"`
}

type BeforeCompactRequest struct {
	Reason       string             `json:"reason"`
	WillRetry    bool               `json:"willRetry"`
	Instructions string             `json:"instructions,omitempty"`
	Preparation  CompactPreparation `json:"preparation"`
}

type CustomCompactResult struct {
	Summary string       `json:"summary"`
	Usage   *types.Usage `json:"usage,omitempty"`
	Details any          `json:"details,omitempty"`
}

type BeforeCompactDecision struct {
	Cancel bool                 `json:"cancel,omitempty"`
	Result *CustomCompactResult `json:"result,omitempty"`
}

type ProviderRequest struct {
	Messages       []types.Message `json:"messages"`
	Tools          []toolapi.Spec  `json:"tools"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	MaxTokens      int             `json:"maxTokens,omitzero"`
	ThinkingEffort string          `json:"thinkingEffort,omitempty"`
}

type ProviderStreamRequest struct {
	Provider   string              `json:"provider"`
	Model      provider.Model      `json:"model"`
	Credential provider.Credential `json:"credential"`
	Request    loop.Request        `json:"request"`
}

type ProviderCompactRequest struct {
	Provider   string              `json:"provider"`
	Model      provider.Model      `json:"model"`
	Credential provider.Credential `json:"credential"`
	Request    loop.Request        `json:"request"`
}

type ProviderCompactResult struct {
	Items []json.RawMessage `json:"items"`
	Usage *types.Usage      `json:"usage,omitempty"`
}

type ProviderStreamEvent struct {
	RequestID      string            `json:"requestId"`
	Type           string            `json:"type"`
	Delta          string            `json:"delta,omitempty"`
	ContentIndex   int               `json:"contentIndex,omitzero"`
	ToolCallID     string            `json:"toolCallId,omitempty"`
	ToolName       string            `json:"toolName,omitempty"`
	ToolCall       *types.Content    `json:"toolCall,omitempty"`
	Message        *types.Message    `json:"message,omitempty"`
	ResponsesItems []json.RawMessage `json:"responsesItems,omitempty"`
	Reason         string            `json:"reason,omitempty"`
	Error          string            `json:"error,omitempty"`
	NonRetryable   bool              `json:"nonRetryable,omitempty"`
	RetryAfter     int64             `json:"retryAfter,omitzero"`
}

type ProviderAuthRequest struct {
	RequestID  string              `json:"requestId"`
	Provider   string              `json:"provider"`
	Mode       string              `json:"mode,omitempty"`
	Credential provider.Credential `json:"credential,omitzero"`
	Value      string              `json:"value,omitempty"`
}

type ProviderAuthEvent struct {
	RequestID        string               `json:"requestId"`
	Provider         string               `json:"provider"`
	Type             string               `json:"type"`
	URL              string               `json:"url,omitempty"`
	Instructions     string               `json:"instructions,omitempty"`
	UserCode         string               `json:"userCode,omitempty"`
	VerificationURI  string               `json:"verificationUri,omitempty"`
	IntervalSeconds  int                  `json:"intervalSeconds,omitzero"`
	ExpiresInSeconds int                  `json:"expiresInSeconds,omitzero"`
	Credential       *provider.Credential `json:"credential,omitempty"`
	Error            string               `json:"error,omitempty"`
}

type ProviderAuthResult struct {
	Accepted   bool                 `json:"accepted,omitzero"`
	Refreshed  bool                 `json:"refreshed,omitzero"`
	Credential *provider.Credential `json:"credential,omitempty"`
}

type ShortCircuit struct {
	Text string `json:"text"`
}

type Fallback struct {
	Text string `json:"text,omitempty"`
	Skip bool   `json:"skip,omitzero"`
}

type HTTPRequestView struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type HTTPRequestPatch struct {
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (r *Registration) indexSubscriptions() {
	r.syncEvents = map[string]bool{}
	r.asyncEvents = map[string]bool{}
	for _, sub := range r.Subscriptions {
		if AcceptSubscription(sub) != nil {
			continue
		}
		if Mode(sub.Mode) == ModeSync {
			r.syncEvents[sub.Event] = true
		} else {
			r.asyncEvents[sub.Event] = true
		}
	}
}

func (r Registration) hasSync(event string) bool {
	return r.syncEvents[event]
}

func (r Registration) hasAsync(event string) bool {
	return r.asyncEvents[event]
}

type Interceptor interface {
	BeforeRun(ctx context.Context, system string, msgs []types.Message) (string, []types.Message, error)
	TransformContext(ctx context.Context, msgs []types.Message) ([]types.Message, error)
	BeforeTool(ctx context.Context, in ToolCall) (ToolCall, *Block, error)
	AfterTool(ctx context.Context, in ToolCall, res ResultPatch) (ResultPatch, error)
	BeforeProvider(ctx context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error)
	BeforeProviderHTTP(ctx context.Context, view HTTPRequestView) (HTTPRequestPatch, error)
	AfterProviderHTTP(ctx context.Context, status int, headers map[string]string) error
	AfterProviderError(ctx context.Context, errClass string) (Fallback, error)
	OnEvent(ctx context.Context, ev Event) error
}

type NopInterceptor struct{}

func (NopInterceptor) BeforeRun(_ context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
	return system, msgs, nil
}

func (NopInterceptor) TransformContext(_ context.Context, msgs []types.Message) ([]types.Message, error) {
	return msgs, nil
}

func (NopInterceptor) BeforeTool(_ context.Context, in ToolCall) (ToolCall, *Block, error) {
	return in, nil, nil
}

func (NopInterceptor) AfterTool(_ context.Context, _ ToolCall, res ResultPatch) (ResultPatch, error) {
	return res, nil
}

func (NopInterceptor) BeforeProvider(_ context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error) {
	return req, nil, nil
}

func (NopInterceptor) BeforeProviderHTTP(_ context.Context, _ HTTPRequestView) (HTTPRequestPatch, error) {
	return HTTPRequestPatch{}, nil
}

func (NopInterceptor) AfterProviderHTTP(context.Context, int, map[string]string) error { return nil }

func (NopInterceptor) AfterProviderError(context.Context, string) (Fallback, error) {
	return Fallback{}, nil
}

func (NopInterceptor) OnEvent(context.Context, Event) error { return nil }

type ErrorFunc func(sessionID, name, capability, code, message string)

func redactMessages(msgs []types.Message) []types.Message {
	out := make([]types.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].ContextOnly = false
		cs := make([]types.Content, 0, len(m.Content))
		for _, c := range m.Content {
			if c.Type == "image" {
				c.Data = ""
				cs = append(cs, c)
				continue
			}
			cs = append(cs, c)
		}
		out[i].Content = cs
	}
	return out
}

func viewHTTP(req *http.Request) HTTPRequestView {
	h := map[string]string{}
	if req == nil {
		return HTTPRequestView{Headers: h}
	}
	for k, v := range req.Header {
		lk := http.CanonicalHeaderKey(k)
		if lk == "Authorization" || lk == "X-Api-Key" || lk == "Cookie" {
			continue
		}
		if len(v) > 0 {
			h[k] = v[0]
		}
	}
	url := ""
	if req.URL != nil {
		url = req.URL.String()
	}
	return HTTPRequestView{Method: req.Method, URL: url, Headers: h}
}

func secretHTTPHeader(k string) bool {
	lk := http.CanonicalHeaderKey(k)
	return lk == "Authorization" || lk == "X-Api-Key" || lk == "Cookie"
}
