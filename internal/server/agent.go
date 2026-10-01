package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"ki/internal/agent"
	"ki/internal/session"
	"ki/internal/telemetry"
	"ki/internal/types"
)

// SpawnAgent implements agent.Runtime. Child agents are sessions rather
// than in-process message arrays: CreateChild links the child to its parent
// (forkMode=tree) so session deletion and sidebar nesting own the relationship,
// while fork_turns selects complete finished history. The in-flight parent
// user turn is never inherited as the child's task.
func (s *Server) SpawnAgent(ctx context.Context, req agent.Request) (agent.Launch, error) {
	if s.agentTasks == nil {
		return agent.Launch{}, errAgentTaskStoreUnavailable
	}
	parent, err := s.open(req.ParentSessionID)
	if err != nil {
		return agent.Launch{}, err
	}
	defer func() { _ = parent.Close() }()
	if err := agent.ValidateTaskName(req.TaskName); err != nil {
		return agent.Launch{}, err
	}
	root, parentPath, err := s.agentTasks.Identity(parent.ID())
	if err != nil {
		return agent.Launch{}, err
	}
	req.RootSessionID = root
	req.TaskPath = parentPath + "/" + req.TaskName
	release, err := s.agentTasks.ReservePath(root, req.TaskPath)
	if err != nil {
		return agent.Launch{}, err
	}
	defer release()
	turns, err := agent.ParseForkTurns(req.ForkTurns)
	if err != nil {
		return agent.Launch{}, err
	}
	child, err := s.newAgentChild(parent, turns)
	if err != nil {
		return agent.Launch{}, fmt.Errorf("create agent session: %w", err)
	}
	childID := child.ID()
	childDir := child.Dir
	rec, attached := s.ws.Match(child.Header.CWD)
	if attached {
		_ = s.ws.AttachSession(rec.ID, childID)
	}
	s.sidx.Add(childID, childDir)
	cleanupChild := func() {
		_ = child.Close()
		if attached {
			_ = s.ws.DetachSession(rec.ID, childID)
		}
		s.dropSessionSnap(childID)
		telemetry.Forget(childDir)
		_ = session.Remove(childDir)
		s.sidx.Remove(childID)
	}

	// Why: CreateChild copies the active provider/model. Agent delegation must
	// stay on that provider so a child cannot silently cross credentials or
	// protocol boundaries through a model override.
	_ = child.Close()

	outputFile := filepath.Join(childDir, "events.jsonl")
	// The child's first user message is the delegating agent's prompt plus the
	// subagent envelope; the tool result echoes the prompt as the caller wrote
	// it, so the master's context is not restating the envelope.
	req.Prompt = fmt.Sprintf("You are %s, a child agent of %s. The task below comes from that agent. Final results are delivered automatically; use send_message to request a decision.\n\n%s", req.TaskPath, parentPath, req.Prompt)
	req.SessionID = childID
	req.MetadataPath = filepath.Join(childDir, "agent.json")
	req.OutputFile = outputFile
	launch, err := s.agentTasks.Start(ctx, req, outputFile, s.agentRunner(childID, req))
	if err != nil {
		cleanupChild()
		return agent.Launch{}, fmt.Errorf("start agent task: %w", err)
	}
	launch.SessionID = childID
	launch.TaskName = req.TaskName
	launch.TaskPath = req.TaskPath
	s.agentTasks.SetSessionID(launch.TaskID, childID)
	return launch, nil
}

func (s *Server) newAgentChild(parent *session.Session, turns int) (*session.Session, error) {
	if turns == 0 {
		return session.CreateChild(s.cfg.Sessions.Root, parent)
	}
	if boundary, ok := parent.LastUserBoundary(); ok {
		if turns < 0 {
			return session.ForkHistoryAt(s.cfg.Sessions.Root, parent, boundary, session.ForkModeTree)
		}
		return session.ForkRecentHistoryAt(s.cfg.Sessions.Root, parent, boundary, turns)
	}
	return session.CreateChild(s.cfg.Sessions.Root, parent)
}

func (s *Server) agentRunner(childID string, base agent.Request) agent.Run {
	return func(ctx context.Context, taskID, prompt string) (agent.Completion, error) {
		req := base
		req.SessionID = childID
		req.Prompt = prompt
		snapshot, exists := s.agentTasks.Get(taskID)
		if !exists {
			return agent.Completion{}, os.ErrNotExist
		}
		req.ClientRequestID = snapshot.ClientRequestID
		completion, runErr := s.runChildAgent(ctx, childID, req)
		// Every detached generation reports to its structural parent.
		{
			// Session deletion removes the logical task while its cancelled
			// callback may still be unwinding. Do not enqueue a completion for
			// a child that no longer exists.
			identity := types.CompletionIdentity{TaskID: taskID, Generation: snapshot.Generation}
			if s.agentTasks.CompletionDelivered(identity) {
				return completion, runErr
			}
			outputFile := base.OutputFile
			if outputFile == "" {
				if dir, ok := s.sidx.Lookup(childID); ok {
					outputFile = filepath.Join(dir, "events.jsonl")
				}
			}
			//nolint:contextcheck // completion notify/dispatch must outlive the child run ctx
			s.notifyAgentCompletion(base.ParentSessionID, identity, base.Description, outputFile, completion, runErr)
		}
		return completion, runErr
	}
}

func (s *Server) restoreAgentTasks(infos []session.Info) {
	restored := []agent.Metadata{}
	for _, info := range infos {
		metadataPath := filepath.Join(info.Dir, "agent.json")
		basePath := metadataPath
		metadata, readErr := agent.ReadMetadata(metadataPath)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				slog.Warn("read agent metadata", "path", metadataPath, "err", readErr)
			}
			continue
		}
		loaded, err := s.agentTasks.LoadMetadata(metadataPath, func(ctx context.Context, taskID, prompt string) (agent.Completion, error) {
			meta, readErr := agent.ReadMetadata(basePath)
			if readErr != nil {
				return agent.Completion{}, readErr
			}
			base := agent.Request{
				Description:     meta.Description,
				ParentSessionID: meta.ParentSessionID,
				SessionID:       meta.SessionID, MetadataPath: metadataPath, OutputFile: meta.OutputFile,
			}
			return s.agentRunner(meta.SessionID, base)(ctx, taskID, prompt)
		})
		if err != nil {
			slog.Warn("restore agent metadata", "path", metadataPath, "err", err)
			continue
		}
		if loaded {
			restored = append(restored, metadata)
		}
	}
	for _, metadata := range restored {
		if _, _, err := s.agentTasks.Identity(metadata.SessionID); err != nil {
			slog.Warn("restore agent identity", "session_id", metadata.SessionID, "error", err)
		}
	}
	for _, metadata := range restored {
		if _, err := s.agentTasks.ResumePending(metadata.TaskID); err != nil {
			slog.Warn("resume pending agent message", "task_id", metadata.TaskID, "error", err)
		}
	}

}

func (s *Server) notifyAgentCompletion(parentID string, identity types.CompletionIdentity, description, outputFile string, completion agent.Completion, runErr error) {
	if parentID == "" {
		return
	}
	taskID := identity.TaskID
	status := "completed"
	result := completion.Result
	if task, ok := s.agentTasks.Get(taskID); ok && task.Status == agent.Killed {
		status = "stopped"
		result = task.Error
	} else if task, ok := s.agentTasks.Get(taskID); ok && task.Status == agent.Interrupted {
		status = "interrupted"
		result = task.Error
	} else if errors.Is(runErr, context.Canceled) {
		status = "cancelled"
		if task, ok := s.agentTasks.Get(taskID); ok {
			reason, source := runCancellation(s.runAt(task.SessionID))
			result = reason
			if source != "" {
				result += " (" + source + ")"
			}
		}
		if result == "" {
			result = "cancelled"
		}
	} else if runErr != nil {
		status = "failed"
		result = runErr.Error()
	}
	text := fmt.Sprintf("<task-notification>\nTask %s (%s) %s.\nResult:\n%s\noutput_file: %s\n</task-notification>", taskID, description, status, result, outputFile)
	if errors.Is(runErr, context.Canceled) {
		return
	}
	if s.agentTasks.CompletionDelivered(identity) {
		return
	}
	if _, err := s.acceptAgentContext(parentID, "agent:"+taskID, text, &identity); err != nil {
		slog.Warn("persist agent completion", "task_id", taskID, "err", err)
	}

}

// SendAgentMessage applies QueueOnly context or explicit root-scoped follow-up work.
func (s *Server) SendAgentMessage(ctx context.Context, req agent.MessageRequest) (agent.MessageResult, error) {
	return s.dispatchAgentMessage(ctx, req)
}

func (s *Server) runChildAgent(ctx context.Context, id string, req agent.Request) (agent.Completion, error) {
	st, runCtx, err := s.occupy(ctx, id)
	if err != nil {
		return agent.Completion{}, err
	}
	if snapshot, ok := s.agentTasks.TaskForSession(id); ok {
		st.agentTaskID = snapshot.TaskID
		st.agentGeneration = snapshot.Generation
	}
	enableRunInbox(st)
	st.inputMetadata.ClientRequestID = req.ClientRequestID
	// runPrompt owns the child occupy release and persists the complete child
	// transcript. The clean child has no inherited history, so the directive is
	// its first user message.
	s.runPrompt(runCtx, st, id, []types.Content{{Type: "text", Text: req.Prompt}}, nil, "", "agent", "", nil)
	snapshot, ok := s.agentTasks.Get(st.agentTaskID)
	if !ok {
		return agent.Completion{}, os.ErrNotExist
	}
	return agent.Completion{Result: snapshot.Result, ToolUseCount: snapshot.RunStats.Tools, TotalTokens: snapshot.RunStats.TotalTokens}, st.err
}

// Get returns a logical-agent snapshot for host lifecycle operations.
func (s *Server) Get(key string) (agent.Snapshot, bool) {
	if s.agentTasks == nil {
		return agent.Snapshot{}, false
	}
	return s.agentTasks.Get(key)
}

// Wait blocks until an agent task reaches a terminal state or ctx cancels.
func (s *Server) Wait(ctx context.Context, id string) (agent.Snapshot, error) {
	if s.agentTasks == nil {
		return agent.Snapshot{}, errAgentTaskStoreUnavailable
	}
	snap, err := s.agentTasks.Wait(ctx, id)
	if err != nil {
		return snap, fmt.Errorf("wait agent task: %w", err)
	}
	return snap, nil
}

// Stop interrupts a running agent task.
func (s *Server) Stop(id string) (agent.Snapshot, error) {
	if s.agentTasks == nil {
		return agent.Snapshot{}, errAgentTaskStoreUnavailable
	}
	snap, err := s.agentTasks.Stop(id)
	if err != nil {
		return snap, fmt.Errorf("stop agent task: %w", err)
	}
	return snap, nil
}

// commitUserMessage is shared by loop drains, initial queue turns and the final
// canceled-run handoff. Acceptance alone must never consume a completion.
func (s *Server) commitUserMessage(message types.Message, persist func() error) (bool, error) {
	if message.Completion != nil && s.agentTasks != nil {
		return s.agentTasks.CommitNotification(*message.Completion, persist)
	}
	err := persist()
	return err == nil, err
}

// preserveRunInbox also runs on setup errors and the last cancellation race:
// closing the live handoff and taking its suffix must be one atomic operation.
// No completion is claimed here; durable dispatch arbitrates at persistence.
func (s *Server) preserveRunInbox(id string, st *runState) {
	st.mu.Lock()
	st.steerClosed = true
	pending := st.inbox.Take()
	st.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	dir, ok := s.sidx.Lookup(id)
	if !ok {
		return
	}
	for _, message := range pending {
		if message.ContextOnly {
			if _, err := session.EnqueueContext(dir, session.ContextQueuedItem{Message: message, IdempotencyKey: message.ClientRequestID}); err != nil {
				slog.Warn("preserve context mailbox", "session_id", id, "err", err)
			}
			continue
		}
		lane := session.QueueSystemLane
		if message.Origin == "" {
			lane = session.QueueHumanLane
		}
		if _, err := session.EnqueueMessage(dir, message, lane); err != nil {
			slog.Warn("preserve undrained inbox", "session_id", id, "err", err)
		}
	}
	s.publishQueueChanged(id)
}
