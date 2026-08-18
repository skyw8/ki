package tools

import "ki/internal/loop"

// Set is the four built-in tools bound to a session cwd.
type Set struct {
	CWD  string
	Jobs *JobStore
}

// All returns the four tools.
func (s Set) All() []loop.Tool {
	if s.Jobs == nil {
		s.Jobs = NewJobStore()
	}
	cwd := s.CWD
	jobs := s.Jobs
	return []loop.Tool{
		readTool{cwd: cwd, jobs: jobs},
		writeTool{cwd: cwd},
		editTool{cwd: cwd},
		bashTool{cwd: cwd, jobs: jobs},
	}
}
