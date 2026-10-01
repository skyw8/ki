package support

import (
	"ki/internal/telemetry"
	toolapi "ki/internal/tool"
	"ki/internal/types"
)

func Error(s string) toolapi.Result {
	return toolapi.Result{Content: []types.Content{{Type: "text", Text: s}}, IsError: true}
}

func DiagnosticError(s, status, kind, domain string) toolapi.Result {
	res := Error(s)
	res.Diagnostic = telemetry.ToolDiagnostic{Status: status, Kind: kind, FaultDomain: domain}
	return res
}

func Text(s string) toolapi.Result {
	return toolapi.Result{Content: []types.Content{{Type: "text", Text: s}}}
}
