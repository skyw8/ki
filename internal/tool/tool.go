package tool

import (
	"context"
)

// Tool is something the model can call.
type Tool interface {
	Name() string
	Description() string
	Prompt() string
	Snippet() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) Result
}

// ProgressTool is implemented by tools that can report incremental output
// while Execute is still running. The callback is intentionally untyped at the
// loop boundary: tool-specific progress is persisted as JSON and presented to
// the model/UI as a partial result.
type ProgressTool interface {
	Tool
	ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) Result
}

// SpecProvider optionally replaces the default JSON function schema.
// It is used by grammar-backed Responses custom tools such as apply_patch.
type SpecProvider interface {
	ToolSpec() Spec
}

// FreeformTool executes the raw input of a custom tool call.
type FreeformTool interface {
	ExecuteRaw(ctx context.Context, input string) Result
}

// ArgumentDiffConsumer incrementally parses a freeform tool call while
// the provider is still producing its arguments. Results are client previews;
// they never authorize or execute the tool.
type ArgumentDiffConsumer interface {
	Consume(delta string) (any, bool)
	Finish() (any, bool)
}

// ArgumentDiffProvider creates isolated state for one streamed tool call.
type ArgumentDiffProvider interface {
	NewArgumentDiffConsumer() ArgumentDiffConsumer
}

// Validator is the optional pre-execution schema check (P0, pi
// validateToolArguments). Tools may implement it to reject malformed
// arguments before any execution starts.
type Validator interface {
	Validate(args map[string]any) error
}
