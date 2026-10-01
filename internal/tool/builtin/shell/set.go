package shelltools

import (
	"ki/internal/process"
	toolapi "ki/internal/tool"
)

type Set struct {
	CWD       string
	Processes *process.Manager
	Shells    process.ShellRuntime
	PathDirs  []string
}

func (s Set) Build() []toolapi.Tool {
	if s.Processes == nil {
		s.Processes = process.NewManager()
	}
	if s.Shells.Empty() {
		s.Shells = process.DefaultShellRuntime()
	}
	s.Shells = s.Shells.WithPathDirs(s.PathDirs)
	return []toolapi.Tool{execCommandTool{cwd: s.CWD, processes: s.Processes, shells: s.Shells, pathDirs: s.PathDirs}, writeStdinTool{processes: s.Processes}}
}
