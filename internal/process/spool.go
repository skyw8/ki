package process

import (
	"os"
)

type OutputSpool interface {
	CreateOutputFile(sessionID, toolName string) (*os.File, error)
}
