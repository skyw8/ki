package tools

import (
	"context"
	"fmt"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/toolname"
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
	Processes            *ShellProcessManager
	Agent                AgentRuntime
	AgentParentSessionID string
	Shells               ShellRuntime
	ReadOps              ReadOperations
	Mutations            *MutationQueue
	// PathDirs are extension-contributed directories that shell children get on
	// PATH. They are resolved per turn from the session's resource snapshot, so
	// enabling an extension applies with the next prompt.
	PathDirs []string
}

// Build returns the tools exposed for one resolved model.
func (s Set) Build(profile Profile) []loop.Tool {
	if s.Mutations == nil {
		s.Mutations = NewMutationQueue()
	}
	agent := s.Agent
	if agent != nil && s.AgentParentSessionID != "" {
		agent = scopedAgentRuntime{AgentRuntime: agent, sessionID: s.AgentParentSessionID}
	}
	cwd := s.CWD
	processes := s.Processes
	if processes == nil {
		processes = NewShellProcessManager()
	}
	shells := s.Shells
	if shells.bash.kind == "" {
		shells = fallbackShellRuntime()
	}
	// The shell specs carry the extension PATH directories because every shell
	// child builds its environment from the
	// spec it was started with.
	shells.bash.pathDirs = s.PathDirs
	if shells.powerShell != nil {
		powerShell := *shells.powerShell
		powerShell.pathDirs = s.PathDirs
		shells.powerShell = &powerShell
	}
	out := []loop.Tool{readTool{cwd: cwd, rich: profile.RichRead, ops: s.ReadOps}}
	if profile.ApplyPatch {
		out = append(out, applyPatchTool{cwd: cwd, mutations: s.Mutations})
	} else {
		out = append(out, writeTool{cwd: cwd, mutations: s.Mutations}, editTool{cwd: cwd, mutations: s.Mutations})
	}
	out = append(out,
		grepTool{cwd: cwd},
		globTool{cwd: cwd},
	)
	out = append(out, execCommandTool{cwd: cwd, processes: processes, shells: shells, pathDirs: s.PathDirs}, writeStdinTool{processes: processes})
	if agent != nil {
		for _, name := range []string{"spawn_agent", "send_message", "followup_task", "wait_agent", "interrupt_agent", "list_agents"} {
			out = append(out, agentTool{name: name, runtime: agent})
		}
	}
	return out
}

// Catalog returns every built-in that can be selected by a model profile.
// The settings toggle is global, so model-specific editors must remain visible
// when another model is selected or their disabled state would be lost.
func (s Set) Catalog(profile Profile) []loop.Tool {
	classic := s.Build(Profile{RichRead: profile.RichRead})
	patch := s.Build(Profile{RichRead: profile.RichRead, ApplyPatch: true})
	var patchTool loop.Tool
	for _, tool := range patch {
		if tool.Name() == "apply_patch" {
			patchTool = tool
			break
		}
	}
	out := make([]loop.Tool, 0, len(classic)+1)
	for _, tool := range classic {
		out = append(out, tool)
		if tool.Name() == "edit" && patchTool != nil {
			out = append(out, patchTool)
		}
	}
	return out
}

// FilterBuiltins applies a global built-in tool toggle to a tool slice. The
// caller must pass only the tools returned by Set.Build; keeping this helper
// separate from extension filtering prevents a global built-in setting from
// accidentally disabling an extension tool with a matching name.
func FilterBuiltins(all []loop.Tool, toggle session.Toggle) []loop.Tool {
	out := make([]loop.Tool, 0, len(all))
	for _, tool := range all {
		if toolAllowed(toggle, tool.Name()) {
			out = append(out, tool)
		}
	}
	return out
}

type scopedAgentRuntime struct {
	AgentRuntime
	sessionID string
}

func (s scopedAgentRuntime) SpawnAgent(ctx context.Context, req AgentRequest) (AgentLaunch, error) {
	req.ParentSessionID = s.sessionID
	launch, err := s.AgentRuntime.SpawnAgent(ctx, req)
	if err != nil {
		return AgentLaunch{}, fmt.Errorf("spawn agent: %w", err)
	}
	return launch, nil
}

func (s scopedAgentRuntime) SendAgentMessage(ctx context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
	req.SenderSessionID = s.sessionID
	return s.AgentRuntime.SendAgentMessage(ctx, req)
}
func (s scopedAgentRuntime) WaitAgent(ctx context.Context, _ string, timeout time.Duration) (AgentWaitResult, error) {
	return s.AgentRuntime.WaitAgent(ctx, s.sessionID, timeout)
}
func (s scopedAgentRuntime) ListAgents(_ string, prefix string) ([]AgentView, error) {
	return s.AgentRuntime.ListAgents(s.sessionID, prefix)
}
func (s scopedAgentRuntime) InterruptAgent(ctx context.Context, _ string, target string) (AgentView, error) {
	return s.AgentRuntime.InterruptAgent(ctx, s.sessionID, target)
}

func toolAllowed(toggle session.Toggle, name string) bool {
	if len(toggle.Only) > 0 {
		found := false
		for _, n := range toggle.Only {
			if toolname.Equal(n, name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, n := range toggle.Disabled {
		if toolname.Equal(n, name) {
			return false
		}
	}
	return true
}
