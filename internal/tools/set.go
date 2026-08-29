package tools

import (
	"context"
	"fmt"
	"os"

	"ki/internal/loop"
)

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
	CWD   string
	Jobs  *JobStore
	Agent AgentRuntime
	// AgentDepth is the current Agent-created child depth. The main session is
	// depth 0; at MaxAgentDepth the Agent tool is withheld.
	AgentDepth           int
	AgentParentSessionID string
	AgentParentEntryID   string
	Shells               ShellRuntime
	ReadOps              ReadOperations
	Mutations            *MutationQueue
}

// Build returns the tools exposed for one resolved model.
func (s Set) Build(profile Profile) []loop.Tool {
	if s.Jobs == nil {
		s.Jobs = NewJobStore()
	}
	if s.Mutations == nil {
		s.Mutations = NewMutationQueue()
	}
	agent := s.Agent
	if agent != nil && (s.AgentParentSessionID != "" || s.AgentParentEntryID != "") {
		agent = scopedAgentRuntime{AgentRuntime: agent, sessionID: s.AgentParentSessionID, entryID: s.AgentParentEntryID}
	}
	tasks := compositeTaskStore{shell: s.Jobs}
	if agent != nil {
		tasks.agent = agent
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
		taskOutputTool{tasks: tasks},
		taskStopTool{tasks: tasks},
	)
	if shells.bash.available() {
		out = append(out, monitorTool{cwd: cwd, jobs: jobs, shell: shells.bash})
	}
	if agent != nil {
		// Why: a recursive Agent call can otherwise fan out without a bound and
		// exhaust provider, process, or disk resources. Keep SendMessage for a
		// deepest child, but withhold only the spawning capability at the limit.
		if s.AgentDepth < MaxAgentDepth {
			out = append(out, agentTool{runtime: agent})
		}
		if messenger, ok := agent.(AgentMessenger); ok {
			out = append(out, sendMessageTool{messenger: messenger})
		}
	}
	return out
}

type scopedAgentRuntime struct {
	AgentRuntime
	sessionID string
	entryID   string
}

func (s scopedAgentRuntime) SpawnAgent(ctx context.Context, req AgentRequest) (AgentLaunch, error) {
	req.ParentSessionID = s.sessionID
	req.ParentEntryID = s.entryID
	launch, err := s.AgentRuntime.SpawnAgent(ctx, req)
	if err != nil {
		return AgentLaunch{}, fmt.Errorf("spawn agent: %w", err)
	}
	return launch, nil
}

func (s scopedAgentRuntime) SendAgentMessage(ctx context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
	messenger, ok := s.AgentRuntime.(AgentMessenger)
	if !ok {
		return AgentMessageResult{}, os.ErrNotExist
	}
	req.SenderSessionID = s.sessionID
	result, err := messenger.SendAgentMessage(ctx, req)
	if err != nil {
		return AgentMessageResult{}, fmt.Errorf("send agent message: %w", err)
	}
	return result, nil
}

type compositeTaskStore struct {
	shell *JobStore
	agent TaskStore
}

func (s compositeTaskStore) Get(key string) (TaskSnapshot, bool) {
	if s.shell != nil {
		if task, ok := s.shell.Get(key); ok {
			return task, true
		}
	}
	if s.agent != nil {
		return s.agent.Get(key)
	}
	return TaskSnapshot{}, false
}

func (s compositeTaskStore) Wait(ctx context.Context, id string) (TaskSnapshot, error) {
	if s.shell != nil {
		if _, ok := s.shell.Get(id); ok {
			return s.shell.Wait(ctx, id)
		}
	}
	if s.agent != nil {
		snap, err := s.agent.Wait(ctx, id)
		if err != nil {
			return TaskSnapshot{}, fmt.Errorf("wait agent task: %w", err)
		}
		return snap, nil
	}
	return TaskSnapshot{}, os.ErrNotExist
}

func (s compositeTaskStore) Stop(id string) (TaskSnapshot, error) {
	if s.shell != nil {
		if _, ok := s.shell.Get(id); ok {
			return s.shell.Stop(id)
		}
	}
	if s.agent != nil {
		snap, err := s.agent.Stop(id)
		if err != nil {
			return TaskSnapshot{}, fmt.Errorf("stop agent task: %w", err)
		}
		return snap, nil
	}
	return TaskSnapshot{}, os.ErrNotExist
}
