package tool

import (
	"context"
)

type executionContextKey struct{}

// ExecutionIdentity attributes a process to the tool call that started it.
// It is host-only metadata and never expands model-visible schemas.
type ExecutionIdentity struct {
	RunID      string
	CallID     string
	AgentID    string
	Generation uint64
}

func ExecutionFromContext(ctx context.Context) ExecutionIdentity {
	identity, _ := ctx.Value(executionContextKey{}).(ExecutionIdentity)
	return identity
}

// WithExecutionIdentity carries host attribution to tool adapters. Runtime
// services receive their own explicit identity rather than reading this context.
func WithExecutionIdentity(ctx context.Context, identity ExecutionIdentity) context.Context {
	return context.WithValue(ctx, executionContextKey{}, identity)
}
