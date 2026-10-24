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
