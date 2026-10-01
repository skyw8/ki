package loop

import "context"

type executionContextKey struct{}

// ToolExecutionIdentity attributes a process to the tool call that started it.
// It is host-only metadata and never expands model-visible schemas.
type ToolExecutionIdentity struct {
	RunID      string
	CallID     string
	AgentID    string
	Generation uint64
}

func ExecutionFromContext(ctx context.Context) ToolExecutionIdentity {
	identity, _ := ctx.Value(executionContextKey{}).(ToolExecutionIdentity)
	return identity
}
