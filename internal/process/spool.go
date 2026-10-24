package process

import "os"

type OutputFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

type OutputSpool interface {
	CreateOutputFile(sessionID, toolName string) (*os.File, error)
}

type BudgetedOutputSpool interface {
	CreateProcessOutputFile(sessionID, toolName string) (OutputFile, error)
}
