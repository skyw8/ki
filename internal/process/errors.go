package process

import (
	"errors"
)

const (
	maxLines       = 2000
	maxBytes       = 50 * 1024
	shellWaitDelay = ProcessWaitDelay
)

var (
	errTaskStoreClosed        = errors.New("task store is closed")
	errInterpreterUnavailable = errors.New("command interpreter is unavailable")
	errTaskNotRunning         = errors.New("task is not running")
)
