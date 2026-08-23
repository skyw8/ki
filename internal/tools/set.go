package tools

import "ki/internal/loop"

// Editor selects the mutually exclusive model-visible editing interface.
type Editor string

const (
	// EditorWriteEdit exposes Write and Edit tools for ordinary function APIs.
	EditorWriteEdit Editor = "write_edit"
	// EditorApplyPatch exposes the freeform apply_patch tool for Responses.
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
	CWD       string
	Jobs      *JobStore
	Shells    ShellRuntime
	ReadOps   ReadOperations
	Mutations *MutationQueue
}

// Build returns the tools exposed for one resolved model.
func (s Set) Build(profile Profile) []loop.Tool {
	if s.Jobs == nil {
		s.Jobs = NewJobStore()
	}
	if s.Mutations == nil {
		s.Mutations = NewMutationQueue()
	}
	cwd := s.CWD
	jobs := s.Jobs
	shells := s.Shells
	if shells.bash.kind == "" {
		shells = fallbackShellRuntime()
	}
	out := []loop.Tool{readTool{cwd: cwd, rich: profile.RichRead, ops: s.ReadOps}}
	if profile.Editor == EditorApplyPatch {
		out = append(out, applyPatchTool{cwd: cwd, mutations: s.Mutations})
	} else {
		out = append(out, writeTool{cwd: cwd, mutations: s.Mutations}, editTool{cwd: cwd, mutations: s.Mutations})
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
