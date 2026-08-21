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
	CWD  string
	Jobs *JobStore
}

// Build returns the tools exposed for one resolved model.
func (s Set) Build(profile Profile) []loop.Tool {
	if s.Jobs == nil {
		s.Jobs = NewJobStore()
	}
	cwd := s.CWD
	jobs := s.Jobs
	out := []loop.Tool{readTool{cwd: cwd, jobs: jobs, rich: profile.RichRead}}
	if profile.Editor == EditorApplyPatch {
		out = append(out, applyPatchTool{cwd: cwd})
	} else {
		out = append(out, writeTool{cwd: cwd}, editTool{cwd: cwd})
	}
	return append(out, grepTool{cwd: cwd}, globTool{cwd: cwd}, bashTool{cwd: cwd, jobs: jobs})
}
