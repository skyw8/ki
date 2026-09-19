package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
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
// than in-process message arrays: CreateChild links the child to its parent
// (forkMode=tree) so session deletion and sidebar nesting own the relationship,
// while the child starts without the parent transcript. A delegated agent must
// only receive the directive, not replay the parent's user turn.
func (s *Server) SpawnAgent(ctx context.Context, req tools.AgentRequest) (tools.AgentLaunch, error) {
	if s.agentTasks == nil {
		return tools.AgentLaunch{}, errAgentTaskStoreUnavailable
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
		return tools.AgentLaunch{}, fmt.Errorf("%w %d reached", errMaximumAgentDepth, tools.MaxAgentDepth)
	}
	child, err := s.newAgentChild(parent, req.InheritContext)
	if err != nil {
		return tools.AgentLaunch{}, fmt.Errorf("create agent session: %w", err)
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
	req.Prompt = subagentDirective(parentDepth+1, parent.ID(), req.Prompt)
	req.SessionID = childID
	req.MetadataPath = filepath.Join(childDir, "agent.json")
	req.OutputFile = outputFile
	launch, err := s.agentTasks.Start(ctx, req, outputFile, s.agentRunner(childID, req))
	if err != nil {
		cleanupChild()
		return tools.AgentLaunch{}, fmt.Errorf("start agent task: %w", err)
	}
	launch.SessionID = childID
	s.agentTasks.SetSessionID(launch.TaskID, childID)
	return launch, nil
}

// subagentDirective wraps the delegating agent's prompt into the child's first
// user message. A child is a fresh model call that cannot see the parent's
// instructions, so it is told what it is and which session assigned the task.
// Keeping this in the message rather than in the child's system prompt leaves
// that prompt byte-identical to the parent's, which lets the provider reuse the
// parent's cached prefix for the inherited history.
//
// The result travels back through the Agent tool result or the child's
// completion notification, so the envelope does not ask the child to report:
// SendMessage stays available (its own tool description lists the addresses) for
// a child that needs to ask its caller something mid-task.
//
// The depth shown is the child's own, one below the spawning session.
//
// A child spawned at the depth limit is also told, here, not to delegate. The
// Agent tool stays in its tool set anyway: withholding it would change the tool
// schemas and the system prompt's tool list, and a changed prefix defeats the
// cache reuse this envelope exists to protect. A call past the limit therefore
// fails at the spawn boundary with a "maximum agent depth" tool result.
func subagentDirective(depth int, parentSessionID, prompt string) string {
	head := fmt.Sprintf("You are a subagent at depth %d, started by another agent through the Agent tool; the task below came from that agent", depth)
	if parentSessionID != "" {
		head += fmt.Sprintf(" (session %s)", parentSessionID)
	}
	head += "."
	if depth >= tools.MaxAgentDepth {
		head += fmt.Sprintf(" You are at the maximum nesting depth (%d), so do not call the Agent tool: complete this task yourself.", tools.MaxAgentDepth)
	}
	return head + "\n\n" + prompt
}

// newAgentChild creates the delegated child session. With inheritContext the
// child is seeded with the parent's finished history: ForkAt copies the entry
// chain up to, but not including, the user message that triggered the in-flight
// turn, because that message was addressed to the parent. Without it the child
// starts empty. Both forms keep the parent edge and tree fork mode so nesting,
// deletion, and provider boundaries are unchanged.
func (s *Server) newAgentChild(parent *session.Session, inheritContext bool) (*session.Session, error) {
	if inheritContext {
		if boundary, ok := parent.LastUserBoundary(); ok {
			return session.ForkHistoryAt(s.cfg.Sessions.Root, parent, boundary, session.ForkModeTree)
		}
	}
	return session.CreateChild(s.cfg.Sessions.Root, parent)
}

// Background implements tools.AgentRuntime for the Agent tool's foreground
// timeout: the child keeps running and its completion reaches the parent through
// the background notification path.
func (s *Server) Background(id string) (tools.TaskSnapshot, error) {
	return s.agentTasks.Background(id)
}

// agentDepth counts Agent-created sessions in the current session's durable
// parent chain. Counting the agent.json markers instead of trusting a caller-
// supplied depth keeps the limit valid after restart and for direct runtime
// calls, while ordinary user-created forks do not consume Agent depth.
//
// A parent that no longer exists (the user deleted it, orphaning this session
// the way the sidebar already tolerates) simply ends the walk: failing closed
// here withheld the Agent tool from every descendant of a deleted session
// forever. Cycles and unreadable parents are still errors.
func (s *Server) agentDepth(sess *session.Session) (int, error) {
	if sess == nil {
		return 0, errAgentDepthRequiresSession
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
			return 0, fmt.Errorf("%w at %s", errSessionParentCycle, parentID)
		}
		seen[parentID] = struct{}{}
		ancestor, err := s.open(parentID)
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				return depth, nil
			}
			return 0, fmt.Errorf("open parent session %s while resolving agent depth: %w", parentID, err)
		}
		opened = append(opened, ancestor)
		current = ancestor
	}
}

func (s *Server) agentRunner(childID string, base tools.AgentRequest) tools.AgentRun {
	return func(ctx context.Context, taskID, prompt string, background bool) (tools.AgentCompletion, error) {
		req := base
		req.SessionID = childID
		req.Prompt = prompt
		req.RunInBackground = background
		completion, runErr := s.runChildAgent(ctx, childID, req)
		// A foreground child promoted to background after agentForegroundTimeout
		// must still notify the parent; the run itself is unchanged.
		if background || s.agentTasks.Backgrounded(taskID) {
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
			//nolint:contextcheck // completion notify/dispatch must outlive the child run ctx
			s.notifyAgentCompletion(base.ParentSessionID, taskID, base.Description, outputFile, completion, runErr)
		}
		return completion, runErr
	}
}

func (s *Server) restoreAgentTasks(infos []session.Info) {
	for _, info := range infos {
		metadataPath := filepath.Join(info.Dir, "agent.json")
		basePath := metadataPath
		data, readErr := os.ReadFile(metadataPath) //nolint:gosec // path is agent.json under an indexed session directory
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
			data, readErr := os.ReadFile(basePath) //nolint:gosec // path is agent.json under an indexed session directory
			if readErr != nil {
				return tools.AgentCompletion{}, readErr
			}
			var meta tools.AgentMetadata
			if unmarshalErr := json.Unmarshal(data, &meta); unmarshalErr != nil {
				return tools.AgentCompletion{}, unmarshalErr
			}
			base := tools.AgentRequest{
				Description:     meta.Description,
				ParentSessionID: meta.ParentSessionID,
				SessionID:       meta.SessionID, MetadataPath: metadataPath, OutputFile: meta.OutputFile,
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
	if _, err := session.EnqueueSystem(dir, content, "agent:"+taskID); err != nil {
		return
	}
	s.publishQueueChanged(parentID)
	s.dispatchQueue(parentID)
}

// SendAgentMessage implements the ordinary Agent follow-up protocol. A live
// child is steered through its captured run Inbox; if that run has just ended,
// AgentStore performs the race-safe queue or transcript resume. The target may
// also be a reserved address resolved from the sender's session chain.
func (s *Server) SendAgentMessage(ctx context.Context, req tools.AgentMessageRequest) (tools.AgentMessageResult, error) {
	if s.agentTasks == nil {
		return tools.AgentMessageResult{}, errAgentTaskStoreUnavailable
	}
	target := cmp.Or(strings.TrimSpace(req.Target), tools.AgentTargetParent)
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return tools.AgentMessageResult{}, errAgentMessageRequired
	}
	if target == tools.AgentTargetParent || target == tools.AgentTargetMain {
		sessionID, err := s.resolveSessionTarget(req.SenderSessionID, target)
		if err != nil {
			return tools.AgentMessageResult{}, err
		}
		if sessionID == req.SenderSessionID {
			return tools.AgentMessageResult{}, errAgentSelfMessage
		}
		if task, ok := s.agentTasks.TaskForSession(sessionID); ok {
			return s.messageAgent(ctx, task.TaskID, message)
		}
		return s.queueSessionMessage(sessionID, "agent:"+req.SenderSessionID, message)
	}
	return s.messageAgent(ctx, target, message)
}

// resolveSessionTarget maps a reserved SendMessage address to a session id by
// walking the sender's parent chain: "parent" is the immediate parent session,
// "main" is the root of the chain.
func (s *Server) resolveSessionTarget(senderSessionID, target string) (string, error) {
	if senderSessionID == "" {
		return "", errAgentSenderUnknown
	}
	sess, err := s.open(senderSessionID)
	if err != nil {
		return "", fmt.Errorf("open sender session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	if target == tools.AgentTargetParent {
		parent := strings.TrimSpace(sess.Header.ParentSession)
		if parent == "" {
			return "", errSessionNoParent
		}
		return parent, nil
	}
	opened := make([]*session.Session, 0, 3)
	defer func() {
		for _, extra := range opened {
			_ = extra.Close()
		}
	}()
	current := sess
	seen := map[string]struct{}{sess.ID(): {}}
	for {
		parent := strings.TrimSpace(current.Header.ParentSession)
		if parent == "" {
			return current.ID(), nil
		}
		if _, ok := seen[parent]; ok {
			return "", fmt.Errorf("%w at %s", errSessionParentCycle, parent)
		}
		seen[parent] = struct{}{}
		next, err := s.open(parent)
		if err != nil {
			// A deleted ancestor ends the chain: the topmost session that still
			// exists is the closest thing to "main" this session has.
			if errors.Is(err, session.ErrSessionNotFound) {
				return current.ID(), nil
			}
			return "", fmt.Errorf("open parent session %s: %w", parent, err)
		}
		opened = append(opened, next)
		current = next
	}
}

// messageAgent steers a live agent run, or queues or resumes its next turn.
func (s *Server) messageAgent(ctx context.Context, taskID, message string) (tools.AgentMessageResult, error) {
	task, ok := s.agentTasks.Get(taskID)
	if !ok {
		return tools.AgentMessageResult{}, fmt.Errorf("%w: %s", errAgentNotFound, taskID)
	}
	if task.Status == tools.TaskRunning {
		if live := s.runAt(task.SessionID); live != nil {
			if s.pushSteerRun(live, []types.Content{{Type: "text", Text: message}}) {
				return tools.AgentMessageResult{AgentID: task.TaskID, Status: "steered", Message: "message delivered at the next model round"}, nil
			}
		}
	}
	status, err := s.agentTasks.QueueOrResume(ctx, task.TaskID, message)
	if err != nil {
		return tools.AgentMessageResult{}, fmt.Errorf("queue or resume agent: %w", err)
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

// queueSessionMessage delivers to a session without an agent task: the
// top-level session, or any session in the sender's chain. A live run is
// steered in place; otherwise the message is queued and dispatched as a prompt.
func (s *Server) queueSessionMessage(sessionID, origin, message string) (tools.AgentMessageResult, error) {
	content := []types.Content{{Type: "text", Text: message}}
	if live := s.runAt(sessionID); live != nil && s.pushSteerRun(live, content) {
		return tools.AgentMessageResult{AgentID: sessionID, Status: "steered", Message: "message delivered at the next model round"}, nil
	}
	dir, ok := s.sidx.Lookup(sessionID)
	if !ok {
		return tools.AgentMessageResult{}, fmt.Errorf("%w: %s", errAgentNotFound, sessionID)
	}
	if _, err := session.EnqueueSystem(dir, content, origin); err != nil {
		return tools.AgentMessageResult{}, err
	}
	s.publishQueueChanged(sessionID)
	s.dispatchQueue(sessionID)
	return tools.AgentMessageResult{AgentID: sessionID, Status: "queued", Message: "message queued for the session"}, nil
}

func (s *Server) runChildAgent(ctx context.Context, id string, req tools.AgentRequest) (tools.AgentCompletion, error) {
	st, runCtx, err := s.occupy(ctx, id)
	if err != nil {
		return tools.AgentCompletion{}, err
	}
	enableRunInbox(st)
	// runPrompt owns the child occupy release and persists the complete child
	// transcript. The clean child has no inherited history, so the directive is
	// its first user message.
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

// Get returns one agent task snapshot from the unified TaskStore surface.
// The tools.Set composite store adds shell jobs alongside it for TaskOutput
// and TaskStop.
func (s *Server) Get(key string) (tools.TaskSnapshot, bool) {
	if s.agentTasks == nil {
		return tools.TaskSnapshot{}, false
	}
	return s.agentTasks.Get(key)
}

// Wait blocks until an agent task reaches a terminal state or ctx cancels.
func (s *Server) Wait(ctx context.Context, id string) (tools.TaskSnapshot, error) {
	if s.agentTasks == nil {
		return tools.TaskSnapshot{}, errAgentTaskStoreUnavailable
	}
	snap, err := s.agentTasks.Wait(ctx, id)
	if err != nil {
		return tools.TaskSnapshot{}, fmt.Errorf("wait agent task: %w", err)
	}
	return snap, nil
}

// Stop interrupts a running agent task.
func (s *Server) Stop(id string) (tools.TaskSnapshot, error) {
	if s.agentTasks == nil {
		return tools.TaskSnapshot{}, errAgentTaskStoreUnavailable
	}
	snap, err := s.agentTasks.Stop(id)
	if err != nil {
		return tools.TaskSnapshot{}, fmt.Errorf("stop agent task: %w", err)
	}
	return snap, nil
}
