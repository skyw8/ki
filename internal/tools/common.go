package tools

import "errors"

const (
	maxLines = 2000
	maxBytes = 50 * 1024
)

var errToolExecution = errors.New("tool execution error")
