package tools

import (
	"ki/internal/loop"
	"ki/internal/telemetry"
	"ki/internal/types"
)

func errRes(s string) loop.ToolResult {
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: s}}, IsError: true}
}

func diagnosticErrRes(s, status, kind, domain string) loop.ToolResult {
	res := errRes(s)
	res.Diagnostic = telemetry.ToolDiagnostic{Status: status, Kind: kind, FaultDomain: domain}
	return res
}

func okRes(s string) loop.ToolResult {
	return txt(s)
}
func txt(s string) loop.ToolResult {
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: s}}}
}
