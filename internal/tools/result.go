package tools

import (
	"ki/internal/loop"
	"ki/internal/types"
)

func errRes(s string) loop.ToolResult {
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: s}}, IsError: true}
}

func okRes(s string) loop.ToolResult {
	return txt(s)
}
func txt(s string) loop.ToolResult {
	return loop.ToolResult{Content: []types.Content{{Type: "text", Text: s}}}
}
