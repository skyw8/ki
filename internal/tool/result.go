package tool

import (
	"ki/internal/telemetry"
	"ki/internal/types"
)

type Result struct {
	Content    []types.Content
	IsError    bool
	Details    any
	Diagnostic telemetry.ToolDiagnostic `json:"-"`
	Terminate  bool
}
