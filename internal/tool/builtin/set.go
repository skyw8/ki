package builtin

import (
	"ki/internal/agent"
	"ki/internal/process"
	"ki/internal/session"
	toolapi "ki/internal/tool"
	agenttools "ki/internal/tool/builtin/agent"
	"ki/internal/tool/builtin/catalog"
	filetools "ki/internal/tool/builtin/file"
	shelltools "ki/internal/tool/builtin/shell"
)

// Profile is the provider-neutral subset of model capabilities that affects
// built-in tool exposure. GPT Responses models use the native freeform patch
// editor; other models use the JSON Write/Edit pair.
type Profile struct {
	RichRead   bool
	ApplyPatch bool
}

// Set binds built-in tools to a session cwd.
type Set struct {
	CWD                  string
	Processes            *process.Manager
	Agent                agent.Runtime
	AgentParentSessionID string
	Shells               process.ShellRuntime
	ReadOps              filetools.ReadOperations
	Mutations            *filetools.MutationQueue
	// PathDirs are extension-contributed directories that shell children get on
	// PATH. They are resolved per turn from the session's resource snapshot, so
	// enabling an extension applies with the next prompt.
	PathDirs []string
}

// Catalog returns every built-in that can be selected by a model profile.
// The settings toggle is global, so model-specific editors must remain visible
// when another model is selected or their disabled state would be lost.
func (s Set) Catalog(profile Profile) []toolapi.Tool {
	classic := s.Build(Profile{RichRead: profile.RichRead})
	patch := s.Build(Profile{RichRead: profile.RichRead, ApplyPatch: true})
	var patchTool toolapi.Tool
	for _, tool := range patch {
		if tool.Name() == catalog.ApplyPatch {
			patchTool = tool
			break
		}
	}
	out := make([]toolapi.Tool, 0, len(classic)+1)
	for _, tool := range classic {
		out = append(out, tool)
		if tool.Name() == catalog.Edit && patchTool != nil {
			out = append(out, patchTool)
		}
	}
	return out
}

// FilterBuiltins applies a global built-in tool toggle to a tool slice. The
// caller must pass only the tools returned by Set.Build; keeping this helper
// separate from extension filtering prevents a global built-in setting from
// accidentally disabling an extension tool with a matching name.
func FilterBuiltins(all []toolapi.Tool, toggle session.Toggle) []toolapi.Tool {
	out := make([]toolapi.Tool, 0, len(all))
	for _, tool := range all {
		if toolAllowed(toggle, tool.Name()) {
			out = append(out, tool)
		}
	}
	return out
}

func toolAllowed(toggle session.Toggle, name string) bool {
	if len(toggle.Only) > 0 {
		found := false
		for _, n := range toggle.Only {
			if toolapi.Equal(n, name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, n := range toggle.Disabled {
		if toolapi.Equal(n, name) {
			return false
		}
	}
	return true
}

// Build returns the tools exposed for one resolved model.
func (s Set) Build(profile Profile) []toolapi.Tool {
	out := (filetools.Set{CWD: s.CWD, ReadOps: s.ReadOps, Mutations: s.Mutations}).Build(profile.RichRead, profile.ApplyPatch)
	out = append(out, (shelltools.Set{CWD: s.CWD, Processes: s.Processes, Shells: s.Shells, PathDirs: s.PathDirs}).Build()...)
	return append(out, agenttools.Build(s.Agent, s.AgentParentSessionID)...)
}
