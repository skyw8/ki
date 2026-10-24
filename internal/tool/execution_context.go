package tool

import (
	"context"
)

type executionContextKey struct{}

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

func WithExecutionIdentity(ctx context.Context, identity ExecutionIdentity) context.Context {
	return context.WithValue(ctx, executionContextKey{}, identity)
}
