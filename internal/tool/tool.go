package tool

import (
	"context"
)

type Tool interface {
	Name() string
	Description() string
	Prompt() string
	Snippet() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) Result
}

type ProgressTool interface {
	Tool
	ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) Result
}

type SpecProvider interface {
	ToolSpec() Spec
}

type FreeformTool interface {
	ExecuteRaw(ctx context.Context, input string) Result
}

type ArgumentDiffConsumer interface {
	Consume(delta string) (any, bool)
	Finish() (any, bool)
}

type ArgumentDiffProvider interface {
	NewArgumentDiffConsumer() ArgumentDiffConsumer
}

type Validator interface {
	Validate(args map[string]any) error
}

type ExecutionPolicy struct {
	Group         string
	MaxConcurrent int
	Exclusive     bool
	ReadOnly      bool
	Orchestrator  bool
}

type ExecutionPolicyProvider interface {
	ExecutionPolicy() ExecutionPolicy
}

func PolicyFor(t Tool) ExecutionPolicy {
	if provider, ok := t.(ExecutionPolicyProvider); ok {
		return provider.ExecutionPolicy()
	}
	name := MustCanonical(t.Name())
	switch name {
	case "exec", "wait":
		return ExecutionPolicy{Orchestrator: true}
	case "read", "grep", "glob", "list_agents":
		return ExecutionPolicy{ReadOnly: true}
	default:
		return ExecutionPolicy{}
	}
}
