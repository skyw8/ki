package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"ki/internal/session"
	"ki/internal/tools"
	"ki/internal/types"
)

// SpawnAgent implements tools.AgentRuntime. Child agents are sessions rather
// than in-process message arrays: ForkAt preserves the exact parent branch,
// and forkMode=tree lets the existing session deletion/tree browser logic own
// the resulting parent-child relationship.
func (s *Server) SpawnAgent(ctx context.Context, req tools.AgentRequest) (tools.AgentLaunch, error) {
	if s.agentTasks == nil {
		return tools.AgentLaunch{}, fmt.Errorf("agent task store is unavailable")
	}
	parent, err := s.open(req.ParentSessionID)
	if err != nil {
		return tools.AgentLaunch{}, err
	}
	defer func() { _ = parent.Close() }()
	parentDepth, err := s.agentDepth(parent)
	if err != nil {
		return tools.AgentLaunch{}, err
	}
	if parentDepth >= tools.MaxAgentDepth {
		return tools.AgentLaunch{}, fmt.Errorf("maximum agent depth %d reached", tools.MaxAgentDepth)
	}
	target := req.ParentEntryID
	if target == "" {
		target = parent.LeafID()
		target = resolvedAgentForkTarget(parent, target)
	}
	child, err := session.ForkAt(s.cfg.Sessions.Root, parent, target, session.ForkModeTree)
	if err != nil {
		return tools.AgentLaunch{}, fmt.Errorf("fork agent session: %w", err)
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
		_ = session.Remove(childDir)
		s.sidx.Remove(childID)
	}

	// A model override belongs to the child session config. Persisting it as a
	// model_change entry keeps replay and request_header metadata consistent.
	if strings.TrimSpace(req.Model) != "" {
		ref, model, resolveErr := s.registry.ResolveSpec(req.Model, child.Config.Provider)
		if resolveErr != nil {
			cleanupChild()
			return tools.AgentLaunch{}, resolveErr
		}
		effort, resolveErr := resolveThinking(model, child.Config.ThinkingEffort)
		if resolveErr != nil {
			cleanupChild()
			return tools.AgentLaunch{}, resolveErr
		}
		if err := child.SetModelAndThinking(ref.Provider, ref.Model, effort); err != nil {
			cleanupChild()
			return tools.AgentLaunch{}, err
		}
	}
	_ = child.Close()

	outputFile := filepath.Join(childDir, "events.jsonl")
	req.SessionID = childID
	req.MetadataPath = filepath.Join(childDir, "agent.json")
	req.OutputFile = outputFile
	launch, err := s.agentTasks.Start(ctx, req, outputFile, s.agentRunner(childID, req))
	if err != nil {
		cleanupChild()
		return tools.AgentLaunch{}, err
	}
	launch.SessionID = childID
	s.agentTasks.SetSessionID(launch.TaskID, childID)
	return launch, nil
}

// agentDepth counts Agent-created sessions in the current session's durable
// parent chain. Counting the agent.json markers instead of trusting a caller-
// supplied depth keeps the limit valid after restart and for direct runtime
// calls, while ordinary user-created forks do not consume Agent depth.
func (s *Server) agentDepth(sess *session.Session) (int, error) {
	if sess == nil {
		return 0, fmt.Errorf("agent depth requires a session")
	}
	depth := 0
	current := sess
	opened := make([]*session.Session, 0, 3)
	defer func() {
		for _, ancestor := range opened {
			_ = ancestor.Close()
		}
	}()
	seen := map[string]struct{}{sess.ID(): {}}
	for {
		marker := filepath.Join(current.Dir, "agent.json")
		if _, err := os.Stat(marker); err == nil {
			depth++
		} else if !os.IsNotExist(err) {
			return 0, fmt.Errorf("stat agent metadata for session %s: %w", current.ID(), err)
		}

		parentID := strings.TrimSpace(current.Header.ParentSession)
		if parentID == "" {
			return depth, nil
		}
		if _, ok := seen[parentID]; ok {
			return 0, fmt.Errorf("session parent cycle while resolving agent depth at %s", parentID)
		}
		seen[parentID] = struct{}{}
		ancestor, err := s.open(parentID)
		if err != nil {
			return 0, fmt.Errorf("open parent session %s while resolving agent depth: %w", parentID, err)
		}
		opened = append(opened, ancestor)
		current = ancestor
	}
}

// resolvedAgentForkTarget keeps the current user turn while avoiding an
// assistant tool-call entry whose tool result has not been appended yet. The
// parent is busy executing Agent at that exact boundary; copying that
// unresolved call into the child would produce an invalid provider history.
func resolvedAgentForkTarget(parent *session.Session, target string) string {
	entries, err := parent.EntriesTo(target)
	if err != nil {
		return target
	}
	pending := map[string]bool{}
	for _, entry := range entries {
		if entry.Message == nil {
			continue
		}
		calls := entry.Message.ToolCalls()
		for _, call := range calls {
			if call.ID != "" {
				pending[call.ID] = true
			}
		}
		if entry.Message.Role == "toolResult" && entry.Message.ToolCallID != "" {
			delete(pending, entry.Message.ToolCallID)
		}
		if len(calls) > 0 && len(pending) > 0 {
			return entry.ParentID
		}
	}
	return target
}

func (s *Server) agentRunner(childID string, base tools.AgentRequest) tools.AgentRun {
	return func(ctx context.Context, taskID, prompt string, background bool) (tools.AgentCompletion, error) {
		req := base
		req.SessionID = childID
		req.Prompt = prompt
		req.RunInBackground = background
		completion, runErr := s.runChildAgent(ctx, childID, req)
		if background {
			// Session deletion removes the logical task while its cancelled
			// callback may still be unwinding. Do not enqueue a completion for
			// a child that no longer exists.
			if !s.agentTasks.ClaimNotification(taskID) {
				return completion, runErr
			}
			outputFile := base.OutputFile
			if outputFile == "" {
				if dir, ok := s.sidx.Lookup(childID); ok {
					outputFile = filepath.Join(dir, "events.jsonl")
				}
			}
			s.notifyAgentCompletion(base.ParentSessionID, taskID, base.Description, outputFile, completion, runErr)
		}
		return completion, runErr
	}
}

func (s *Server) restoreAgentTasks(infos []session.Info) {
	for _, info := range infos {
		metadataPath := filepath.Join(info.Dir, "agent.json")
		basePath := metadataPath
		data, readErr := os.ReadFile(metadataPath)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				slog.Warn("read agent metadata", "path", metadataPath, "err", readErr)
			}
			continue
		}
		var metadata tools.AgentMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			slog.Warn("decode agent metadata", "path", metadataPath, "err", err)
			continue
		}
		loaded, err := s.agentTasks.LoadMetadata(metadataPath, func(ctx context.Context, taskID, prompt string, background bool) (tools.AgentCompletion, error) {
			data, readErr := os.ReadFile(basePath)
			if readErr != nil {
				return tools.AgentCompletion{}, readErr
			}
			var meta tools.AgentMetadata
			if unmarshalErr := json.Unmarshal(data, &meta); unmarshalErr != nil {
				return tools.AgentCompletion{}, unmarshalErr
			}
			base := tools.AgentRequest{
				Description: meta.Description, SubagentType: meta.SubagentType,
				Model: meta.Model, ParentSessionID: meta.ParentSessionID,
				SessionID: meta.SessionID, MetadataPath: metadataPath, OutputFile: meta.OutputFile,
			}
			return s.agentRunner(meta.SessionID, base)(ctx, taskID, prompt, background)
		})
		if err != nil {
			slog.Warn("restore agent metadata", "path", metadataPath, "err", err)
			continue
		}
		if loaded {
			if _, resumeErr := s.agentTasks.ResumePending(metadata.TaskID); resumeErr != nil {
				slog.Warn("resume pending agent message", "path", metadataPath, "err", resumeErr)
			}
		}
	}
}

func (s *Server) notifyAgentCompletion(parentID, taskID, description, outputFile string, completion tools.AgentCompletion, runErr error) {
	if parentID == "" {
		return
	}
	status := "completed"
	result := completion.Result
	if task, ok := s.agentTasks.Get(taskID); ok && task.Status == tools.TaskKilled {
		status = "stopped"
		result = task.Error
	} else if task, ok := s.agentTasks.Get(taskID); ok && task.Status == tools.TaskInterrupted {
		status = "interrupted"
		result = task.Error
	} else if runErr != nil {
		status = "failed"
		result = runErr.Error()
	}
	text := fmt.Sprintf("<task-notification>\nTask %s (%s) %s.\nResult:\n%s\noutput_file: %s\n</task-notification>", taskID, description, status, result, outputFile)
	content := []types.Content{{Type: "text", Text: text}}
	// Why: a background Agent tool terminates its parent turn immediately. An
	// Inbox steer accepted in the tiny window before release would then never
	// drain, so completion notifications always take the durable queue path.
	dir, ok := s.sidx.Lookup(parentID)
	if !ok {
		return
	}
	if _, err := session.Enqueue(dir, content); err != nil {
		return
	}
	s.publishQueueChanged(parentID)
	s.dispatchQueue(parentID)
}

// SendAgentMessage implements the ordinary Agent follow-up protocol. A live
// child is steered through its captured run Inbox; if that run has just ended,
// AgentStore performs the race-safe queue or transcript resume.
func (s *Server) SendAgentMessage(ctx context.Context, req tools.AgentMessageRequest) (tools.AgentMessageResult, error) {
	if s.agentTasks == nil {
		return tools.AgentMessageResult{}, fmt.Errorf("agent task store is unavailable")
	}
	target := strings.TrimSpace(req.Target)
	message := strings.TrimSpace(req.Message)
	if target == "" || message == "" {
		return tools.AgentMessageResult{}, fmt.Errorf("to and message are required")
	}
	task, ok := s.agentTasks.Get(target)
	if !ok {
		return tools.AgentMessageResult{}, fmt.Errorf("agent %s not found", target)
	}
	if task.Status == tools.TaskRunning {
		if live := s.runAt(task.SessionID); live != nil {
			if s.pushSteerRun(live, []types.Content{{Type: "text", Text: message}}) {
				return tools.AgentMessageResult{AgentID: task.TaskID, Status: "steered", Message: "message delivered at the next model round"}, nil
			}
		}
	}
	status, err := s.agentTasks.QueueOrResume(target, message)
	if err != nil {
		return tools.AgentMessageResult{}, err
	}
	switch status {
	case "queued":
		return tools.AgentMessageResult{AgentID: task.TaskID, Status: status, Message: "message queued for the current run boundary"}, nil
	case "resumed":
		return tools.AgentMessageResult{AgentID: task.TaskID, Status: status, Message: "agent resumed from its existing session transcript"}, nil
	default:
		return tools.AgentMessageResult{AgentID: task.TaskID, Status: status, Message: "message accepted"}, nil
	}
}

func (s *Server) runChildAgent(ctx context.Context, id string, req tools.AgentRequest) (tools.AgentCompletion, error) {
	st, runCtx, err := s.occupy(ctx, id)
	if err != nil {
		return tools.AgentCompletion{}, err
	}
	enableRunInbox(st)
	// runPrompt owns the child occupy release and persists the complete child
	// transcript. The user directive is appended after the forked history.
	s.runPrompt(runCtx, st, id, []types.Content{{Type: "text", Text: req.Prompt}}, nil, "", "agent", "", nil)
	if st.err != nil {
		return tools.AgentCompletion{}, st.err
	}
	child, err := s.open(id)
	if err != nil {
		return tools.AgentCompletion{}, err
	}
	defer func() { _ = child.Close() }()
	messages := child.MessagesToLeaf()
	var result string
	toolUses := 0
	tokens := 0
	for _, message := range messages {
		if message.Role == "assistant" {
			if text := strings.TrimSpace(message.Text()); text != "" {
				result = text
			}
			toolUses += len(message.ToolCalls())
			if message.Usage != nil {
				tokens += message.Usage.TotalTokens
				if message.Usage.TotalTokens == 0 {
					tokens += message.Usage.Input + message.Usage.Output + message.Usage.CacheRead + message.Usage.CacheWrite
				}
			}
		}
	}
	return tools.AgentCompletion{Result: result, ToolUseCount: toolUses, TotalTokens: tokens}, nil
}

// These methods expose only the agent half of the unified TaskStore. The
// tools.Set composite store adds shell jobs alongside it for TaskOutput and
// TaskStop.
func (s *Server) Get(key string) (tools.TaskSnapshot, bool) {
	if s.agentTasks == nil {
		return tools.TaskSnapshot{}, false
	}
	return s.agentTasks.Get(key)
}

func (s *Server) Wait(ctx context.Context, id string) (tools.TaskSnapshot, error) {
	if s.agentTasks == nil {
		return tools.TaskSnapshot{}, fmt.Errorf("agent task store is unavailable")
	}
	return s.agentTasks.Wait(ctx, id)
}

func (s *Server) Stop(id string) (tools.TaskSnapshot, error) {
	if s.agentTasks == nil {
		return tools.TaskSnapshot{}, fmt.Errorf("agent task store is unavailable")
	}
	return s.agentTasks.Stop(id)
}
