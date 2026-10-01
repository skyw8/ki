package tool

import (
	"ki/internal/telemetry"
	"ki/internal/types"
)

// Result is one tool execution outcome.
type Result struct {
	Content []types.Content
	IsError bool
	Details any
	// Diagnostic is private harness telemetry. Provider adapters receive only
	// the model-facing content and IsError fields.
	Diagnostic telemetry.ToolDiagnostic `json:"-"`
	// Terminate hints the agent to stop after this tool batch when every
	// finalized result in the batch sets it (pi result.terminate).
	Terminate bool
}
