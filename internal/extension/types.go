package extension

import (
	"context"
	"net/http"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

// Event is the redacted DTO delivered to sidecars. No prompt, args, or bodies.
type Event struct {
	Type         string            `json:"type"`
	SessionID    string            `json:"sessionId,omitempty"`
	Role         string            `json:"role,omitempty"`
	Timestamp    int64             `json:"timestamp,omitempty"`
	ToolCallID   string            `json:"toolCallId,omitempty"`
	ToolName     string            `json:"toolName,omitempty"`
	IsError      bool              `json:"isError,omitempty"`
	DurationMs   int64             `json:"durationMs,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	OK           bool              `json:"ok,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	Model        string            `json:"model,omitempty"`
	RunID        string            `json:"runId,omitempty"`
	Text         string            `json:"text,omitempty"`
	StopReason   string            `json:"stopReason,omitempty"`
	ErrorMessage string            `json:"errorMessage,omitempty"`
	ToolTitle    string            `json:"toolTitle,omitempty"`
	External     map[string]string `json:"external,omitempty"`
}

// Registration is the frozen initialize result.
type Registration struct {
	Tools         []ToolSpec                       `json:"tools"`
	Commands      []CommandSpec                    `json:"commands"`
	Providers     []provider.ExtensionProviderSpec `json:"providers"`
	Fallback      bool                             `json:"fallback"`
	Subscriptions []Subscription                   `json:"subscriptions"`
	syncEvents    map[string]bool
	asyncEvents   map[string]bool
}

// ToolSpec is a sidecar-declared tool schema.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Snippet     string         `json:"snippet,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	TimeoutMs   int            `json:"timeoutMs,omitempty"`
}

// CommandSpec is a sidecar-declared executable slash handler.
type CommandSpec struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	ArgumentHint string   `json:"argumentHint,omitempty"`
	Completions  []string `json:"completions,omitempty"`
}

// CatalogEntry is a name/description pair in the read-only extension catalog.
type CatalogEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionContribution is the live tools and commands one sidecar currently
// exposes to a session (initialize result plus session.RegisterTools).
type SessionContribution struct {
	Tools    []CatalogEntry
	Commands []CatalogEntry
}

// ToolCall is one tool_call payload.
type ToolCall struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Args     map[string]any `json:"args"`
	ToolType string         `json:"toolType,omitempty"`
}

// Block stops a tool call.
type Block struct {
	Reason    string `json:"reason"`
	Terminate bool   `json:"terminate,omitempty"`
}

// ResultPatch is tool_result.
type ResultPatch struct {
	Content   []types.Content `json:"content,omitempty"`
	Details   any             `json:"details,omitempty"`
	IsError   *bool           `json:"isError,omitempty"`
	Terminate *bool           `json:"terminate,omitempty"`
}

// ProviderRequest is before_provider_request (no System, no keys).
type ProviderRequest struct {
	Messages       []types.Message `json:"messages"`
	Tools          []loop.ToolSpec `json:"tools"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	MaxTokens      int             `json:"maxTokens,omitempty"`
	ThinkingEffort string          `json:"thinkingEffort,omitempty"`
}

// ProviderStreamRequest is the complete host-to-provider payload. The
// provider sidecar receives the model and opaque credential once, then owns
// request construction and response parsing for the whole stream.
type ProviderStreamRequest struct {
	Provider   string              `json:"provider"`
	Model      provider.Model      `json:"model"`
	Credential provider.Credential `json:"credential"`
	Request    loop.Request        `json:"request"`
}

// ProviderStreamEvent is a compact provider-to-host stream event. Partial
// messages are intentionally reconstructed by the host adapter rather than
// serialized once per token.
type ProviderStreamEvent struct {
	RequestID    string         `json:"requestId"`
	Type         string         `json:"type"`
	Delta        string         `json:"delta,omitempty"`
	ContentIndex int            `json:"contentIndex,omitempty"`
	ToolCallID   string         `json:"toolCallId,omitempty"`
	ToolName     string         `json:"toolName,omitempty"`
	ToolCall     *types.Content `json:"toolCall,omitempty"`
	Message      *types.Message `json:"message,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// ProviderAuthRequest starts or advances provider-owned authentication. The
// host never interprets Credential.Value; it only stores and forwards it.
type ProviderAuthRequest struct {
	RequestID  string              `json:"requestId"`
	Provider   string              `json:"provider"`
	Mode       string              `json:"mode,omitempty"`
	Credential provider.Credential `json:"credential,omitzero"`
	Value      string              `json:"value,omitempty"`
}

// ProviderAuthEvent is a UI-neutral notification emitted by a provider
// sidecar. Credential is present only on the private completed event and is
// never returned by the HTTP status endpoint.
type ProviderAuthEvent struct {
	RequestID        string               `json:"requestId"`
	Provider         string               `json:"provider"`
	Type             string               `json:"type"`
	URL              string               `json:"url,omitempty"`
	Instructions     string               `json:"instructions,omitempty"`
	UserCode         string               `json:"userCode,omitempty"`
	VerificationURI  string               `json:"verificationUri,omitempty"`
	IntervalSeconds  int                  `json:"intervalSeconds,omitempty"`
	ExpiresInSeconds int                  `json:"expiresInSeconds,omitempty"`
	Credential       *provider.Credential `json:"credential,omitempty"`
	Error            string               `json:"error,omitempty"`
}

// ProviderAuthResult is the response to a provider auth RPC. A refresh that
// does not need network access returns Refreshed=false and no credential.
type ProviderAuthResult struct {
	Accepted   bool                 `json:"accepted,omitempty"`
	Refreshed  bool                 `json:"refreshed,omitempty"`
	Credential *provider.Credential `json:"credential,omitempty"`
}

// ShortCircuit skips the live provider call.
type ShortCircuit struct {
	Text string `json:"text"`
}

// Fallback is provider_error.
type Fallback struct {
	Text string `json:"text,omitempty"`
	Skip bool   `json:"skip,omitempty"`
}

// HTTPRequestView is a body-less view of a live HTTP request.
type HTTPRequestView struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// HTTPRequestPatch mutates URL and headers only.
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

// Interceptor is the Host-internal surface implemented by rpcClient
// and _test.go fakes. Authors do not implement this interface.
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

// NopInterceptor is a test stub.
type NopInterceptor struct{}

// BeforeRun implements Interceptor.
func (NopInterceptor) BeforeRun(_ context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
	return system, msgs, nil
}

// TransformContext implements Interceptor.
func (NopInterceptor) TransformContext(_ context.Context, msgs []types.Message) ([]types.Message, error) {
	return msgs, nil
}

// BeforeTool implements Interceptor.
func (NopInterceptor) BeforeTool(_ context.Context, in ToolCall) (ToolCall, *Block, error) {
	return in, nil, nil
}

// AfterTool implements Interceptor.
func (NopInterceptor) AfterTool(_ context.Context, _ ToolCall, res ResultPatch) (ResultPatch, error) {
	return res, nil
}

// BeforeProvider implements Interceptor.
func (NopInterceptor) BeforeProvider(_ context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error) {
	return req, nil, nil
}

// BeforeProviderHTTP implements Interceptor.
func (NopInterceptor) BeforeProviderHTTP(_ context.Context, _ HTTPRequestView) (HTTPRequestPatch, error) {
	return HTTPRequestPatch{}, nil
}

// AfterProviderHTTP implements Interceptor.
func (NopInterceptor) AfterProviderHTTP(context.Context, int, map[string]string) error { return nil }

// AfterProviderError implements Interceptor.
func (NopInterceptor) AfterProviderError(context.Context, string) (Fallback, error) {
	return Fallback{}, nil
}

// OnEvent implements Interceptor.
func (NopInterceptor) OnEvent(context.Context, Event) error { return nil }

// ErrorFunc receives sideband extension_error notifications.
type ErrorFunc func(sessionID, name, capability, code, message string)

var reservedToolNames = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "apply_patch": true,
	"Grep": true, "Glob": true, "Bash": true, "PowerShell": true,
	"TaskOutput": true, "TaskStop": true, "Monitor": true,
}

func redactMessages(msgs []types.Message) []types.Message {
	out := make([]types.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
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
		// Cookie travels with Authorization/X-Api-Key: the HTTP intercept
		// view is headers/URL only and must not leak session credentials.
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
