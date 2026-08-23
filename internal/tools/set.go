package tools

import "ki/internal/loop"

// Editor selects the mutually exclusive model-visible editing interface.
type Editor string

const (
	EditorWriteEdit  Editor = "write_edit"
	EditorApplyPatch Editor = "apply_patch_freeform"
)

// Profile is the provider-neutral subset of model capabilities that affects
// built-in tool exposure. The server derives it from the resolved model.
type Profile struct {
	RichRead bool
	Editor   Editor
}

// Set binds built-in tools to a session cwd.
type Set struct {
	CWD    string
	Jobs   *JobStore
	Shells ShellRuntime
}

// Build returns the tools exposed for one resolved model.
func (s Set) Build(profile Profile) []loop.Tool {
	if s.Jobs == nil {
		s.Jobs = NewJobStore()
	}
	cwd := s.CWD
	jobs := s.Jobs
	shells := s.Shells
	if shells.bash.kind == "" {
		shells = fallbackShellRuntime()
	}
	out := []loop.Tool{readTool{cwd: cwd, rich: profile.RichRead}}
	if profile.Editor == EditorApplyPatch {
		out = append(out, applyPatchTool{cwd: cwd})
	} else {
		out = append(out, writeTool{cwd: cwd}, editTool{cwd: cwd})
	}
	out = append(out,
		grepTool{cwd: cwd},
		globTool{cwd: cwd},
	)
	if shells.bash.available() {
		out = append(out, bashTool{cwd: cwd, jobs: jobs, shell: shells.bash})
	}
	if shells.powerShell != nil {
		out = append(out, powerShellTool{cwd: cwd, jobs: jobs, shell: *shells.powerShell})
	}
	out = append(out,
		taskOutputTool{jobs: jobs},
		taskStopTool{jobs: jobs},
	)
	if shells.bash.available() {
		out = append(out, monitorTool{cwd: cwd, jobs: jobs, shell: shells.bash})
	}
	return out
}
