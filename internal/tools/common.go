package tools

import "errors"

const (
	// maxLines and maxBytes bound one Read page and one shell task tail. Those
	// results carry their own continuation contract (a paging offset or the
	// complete output file), so the loop spill leaves them untouched.
	maxLines = 2000
	maxBytes = 50 * 1024
	// spillSafetyLimit is a memory guard for tools that hand their complete
	// result to the loop spill. It sits far above the model-visible preview,
	// which the store applies; reaching it means the search itself was
	// unbounded and the model is told to narrow it.
	spillSafetyLimit = 16 << 20
)

var errToolExecution = errors.New("tool execution error")
