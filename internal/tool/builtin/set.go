package builtin

import (
	"ki/internal/agent"
	"ki/internal/process"
	"ki/internal/session"
	toolapi "ki/internal/tool"
	agenttools "ki/internal/tool/builtin/agent"
	"ki/internal/tool/builtin/catalog"
	codetools "ki/internal/tool/builtin/code"
	filetools "ki/internal/tool/builtin/file"
	questiontools "ki/internal/tool/builtin/question"
	shelltools "ki/internal/tool/builtin/shell"
	"ki/internal/tool/discovery"
)

type Profile struct {
	RichRead   bool
	ApplyPatch bool
}

type Set struct {
	CWD                   string
	Processes             *process.Manager
	Agent                 agent.Runtime
	AgentParentSessionID  string
	Shells                process.ShellRuntime
	ReadOps               filetools.ReadOperations
	Mutations             *filetools.MutationQueue
	PathDirs              []string
	RequestUserInputAsync questiontools.RequestFunc
}

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
	out = append(out, (codetools.Set{}).Build()...)
	if s.RequestUserInputAsync == nil {
		out = append(out, questiontools.New(nil))
	}
	return append(out, discovery.New(nil).SearchTool())
}

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

func (s Set) Build(profile Profile) []toolapi.Tool {
	out := (filetools.Set{CWD: s.CWD, ReadOps: s.ReadOps, Mutations: s.Mutations}).Build(profile.RichRead, profile.ApplyPatch)
	out = append(out, (shelltools.Set{CWD: s.CWD, Processes: s.Processes, Shells: s.Shells, PathDirs: s.PathDirs}).Build()...)
	out = append(out, agenttools.Build(s.Agent, s.AgentParentSessionID)...)
	if s.RequestUserInputAsync != nil {
		out = append(out, questiontools.New(s.RequestUserInputAsync))
	}
	return out
}
