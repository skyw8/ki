package extension

import "errors"

var (
	errUnknownEvent    = errors.New("unknown lifecycle event")
	errDisallowedSync  = errors.New("sync not allowed for event")
	errDisallowedAsync = errors.New("async not allowed for event")
	errUnknownMode     = errors.New("subscription mode must be sync or async")
	errNoSubscriptions = errors.New("lifecycle capability requires at least one valid subscription")
)

// Lifecycle event names (Host catalog).
const (
	EventBeforeAgentStart      = "before_agent_start"
	EventContext               = "context"
	EventBeforeProviderRequest = "before_provider_request"
	EventBeforeProviderHeaders = "before_provider_headers"
	EventAfterProviderResponse = "after_provider_response"
	EventProviderError         = "provider_error"
	EventToolCall              = "tool_call"
	EventToolResult            = "tool_result"
	EventInput                 = "input"
	EventMessageEnd            = "message_end"
	EventSessionBeforeCompact  = "session_before_compact"
	EventAgentStart            = "agent_start"
	EventAgentEnd              = "agent_end"
	EventAgentSettled          = "agent_settled"
	EventTurnStart             = "turn_start"
	EventTurnEnd               = "turn_end"
	EventMessageStart          = "message_start"
	EventRequestHeader         = "request_header"
	EventToolExecutionStart    = "tool_execution_start"
	EventToolExecutionEnd      = "tool_execution_end"
	EventCompactionStart       = "compaction_start"
	EventCompactionEnd         = "compaction_end"
	EventQueueChanged          = "queue_changed"
	EventSteerAccepted         = "steer_accepted"
	EventRunAborted            = "run_aborted"
	EventMCPServerFailed       = "mcp_server_failed"
	EventMCPToolsChanged       = "mcp_tools_changed"
)

// Mode is a subscription mode.
type Mode string

const (
	ModeSync  Mode = "sync"
	ModeAsync Mode = "async"
)

// Subscription is one initialize subscription.
type Subscription struct {
	Event string `json:"event"`
	Mode  string `json:"mode"`
}

type eventPolicy struct {
	allowSync  bool
	allowAsync bool
}

var eventCatalog = map[string]eventPolicy{
	EventBeforeAgentStart:      {true, true},
	EventContext:               {true, true},
	EventBeforeProviderRequest: {true, true},
	EventBeforeProviderHeaders: {true, true},
	EventAfterProviderResponse: {false, true},
	EventProviderError:         {true, true},
	EventToolCall:              {true, true},
	EventToolResult:            {true, true},
	EventInput:                 {true, true},
	EventMessageEnd:            {true, true},
	EventSessionBeforeCompact:  {true, true},
	EventAgentStart:            {false, true},
	EventAgentEnd:              {false, true},
	EventAgentSettled:          {false, true},
	EventTurnStart:             {false, true},
	EventTurnEnd:               {false, true},
	EventMessageStart:          {false, true},
	EventRequestHeader:         {false, true},
	EventToolExecutionStart:    {false, true},
	EventToolExecutionEnd:      {false, true},
	EventCompactionStart:       {false, true},
	EventCompactionEnd:         {false, true},
	EventQueueChanged:          {false, true},
	EventSteerAccepted:         {false, true},
	EventRunAborted:            {false, true},
	EventMCPServerFailed:       {false, true},
	EventMCPToolsChanged:       {false, true},
}

func catalogPolicy(event string) (eventPolicy, bool) {
	p, ok := eventCatalog[event]
	return p, ok
}

// AcceptSubscription validates one subscription. Returns false if it must be dropped.
func AcceptSubscription(sub Subscription) error {
	p, ok := catalogPolicy(sub.Event)
	if !ok {
		return errUnknownEvent
	}
	switch Mode(sub.Mode) {
	case ModeSync:
		if !p.allowSync {
			return errDisallowedSync
		}
	case ModeAsync:
		if !p.allowAsync {
			return errDisallowedAsync
		}
	default:
		return errUnknownMode
	}
	return nil
}

// CompactCtx is the sync lifecycle context snapshot (no full history).
type CompactCtx struct {
	Idle    bool   `json:"idle"`
	Model   string `json:"model,omitempty"`
	Aborted bool   `json:"aborted"`
}
