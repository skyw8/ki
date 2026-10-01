package process

import (
	"os"
)

// OutputSpool is the tool-output store contract used by shell tasks. Keeping
// it an interface lets tests run without a store while the server roots every
// complete-output file in one session-scoped directory.
type OutputSpool interface {
	CreateOutputFile(sessionID, toolName string) (*os.File, error)
}
