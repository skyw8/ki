package tools

import (
	"context"
	"fmt"
	"os"

	"ki/internal/loop"
	"ki/internal/session"
)

// Profile is the provider-neutral subset of model capabilities that affects
// built-in tool exposure. The server derives it from the resolved model.
// Every model gets the same editing interface (Write/Edit), so the built-in
// tool set is provider-independent apart from rich Read.
type Profile struct {
	RichRead bool
}

// Set binds built-in tools to a session cwd.
type Set struct {
	CWD                  string
	Jobs                 *JobStore
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
	if s.Jobs == nil {
		s.Jobs = NewJobStore()
	}
	if s.Mutations == nil {
		s.Mutations = NewMutationQueue()
	}
	agent := s.Agent
	if agent != nil && s.AgentParentSessionID != "" {
		agent = scopedAgentRuntime{AgentRuntime: agent, sessionID: s.AgentParentSessionID}
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
	// The shell specs carry the extension PATH directories because every shell
	// child (foreground, background, Monitor) builds its environment from the
	// spec it was started with.
	shells.bash.pathDirs = s.PathDirs
	if shells.powerShell != nil {
		powerShell := *shells.powerShell
		powerShell.pathDirs = s.PathDirs
		shells.powerShell = &powerShell
	}
	out := []loop.Tool{readTool{cwd: cwd, rich: profile.RichRead, ops: s.ReadOps}}
	out = append(out, writeTool{cwd: cwd, mutations: s.Mutations}, editTool{cwd: cwd, mutations: s.Mutations})
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
		// The tool set is part of the provider's cached prefix, so it must not
		// depend on how deep this session sits in the Agent chain: withholding
		// Agent at MaxAgentDepth used to change both the tool schemas and the
		// system prompt's tool list, which invalidated the whole inherited
		// prefix (the very reuse that moving a child's identity into its first
		// user message exists to preserve). The depth limit is enforced where
		// the spawn happens (server.SpawnAgent refuses past MaxAgentDepth) and
		// announced in the child's directive envelope.
		out = append(out, agentTool{runtime: agent})
		if messenger, ok := agent.(AgentMessenger); ok {
			out = append(out, sendMessageTool{messenger: messenger})
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
		if toggle.Allowed(tool.Name()) {
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

// MarkNotified routes by ownership: only agent tasks have a completion
// notification to suppress, and the shell store's own marking is a no-op.
func (s compositeTaskStore) MarkNotified(id string) {
	if s.shell != nil {
		if _, ok := s.shell.Get(id); ok {
			s.shell.MarkNotified(id)
			return
		}
	}
	if s.agent != nil {
		s.agent.MarkNotified(id)
	}
}
