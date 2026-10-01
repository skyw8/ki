package server

import (
	"context"
	"fmt"
	"ki/internal/idgen"
	"ki/internal/session"
	"ki/internal/tools"
	"ki/internal/types"
	"strings"
	"time"
)

func (s *Server) WaitAgent(ctx context.Context, caller string, timeout time.Duration) (tools.AgentWaitResult, error) {
	st := s.runAt(caller)
	if st == nil {
		return tools.AgentWaitResult{}, fmt.Errorf("wait_agent requires an active caller turn")
	}
	st.mu.Lock()
	inbox := st.inbox
	st.mu.Unlock()
	if inbox == nil {
		return tools.AgentWaitResult{}, fmt.Errorf("caller mailbox unavailable")
	}
	timedOut, err := inbox.Wait(ctx, timeout)
	reason := inbox.WakeReason()
	message := "mailbox or user input available"
	if timedOut {
		reason = "timeout"
		message = "observation timed out; agents continue running"
	}
	if err != nil {
		reason = "cancelled"
	}
	return tools.AgentWaitResult{WakeReason: reason, TimedOut: timedOut, Message: message}, err
}
func (s *Server) ListAgents(caller, prefix string) ([]tools.AgentView, error) {
	views, err := s.agentTasks.Views(caller, prefix)
	if err != nil {
		return nil, err
	}
	if prefix == "" || prefix == "/root" {
		root, _, err := s.agentTasks.Identity(caller)
		if err != nil {
			return nil, err
		}
		status := tools.TaskStatus("idle")
		if s.running(root) {
			status = tools.TaskRunning
		}
		views = append([]tools.AgentView{{TaskName: "/root", SessionID: root, Status: status}}, views...)
	}
	return views, nil
}
func (s *Server) InterruptAgent(ctx context.Context, caller, target string) (tools.AgentView, error) {
	if err := ctx.Err(); err != nil {
		return tools.AgentView{}, err
	}
	snap, path, err := s.agentTasks.Resolve(caller, target)
	if err != nil {
		return tools.AgentView{}, err
	}
	if path == "/root" || snap.SessionID == caller {
		return tools.AgentView{}, fmt.Errorf("cannot interrupt root or self")
	}
	previous := tools.ViewAgent(snap)
	if snap.Status == tools.TaskRunning || snap.Status == tools.TaskPending {
		_, err = s.agentTasks.Stop(snap.TaskID)
	}
	return previous, err
}

// acceptAgentContext stores QueueOnly input before exposing a live wakeup.
func (s *Server) acceptAgentContext(target, origin, text string, completion *types.CompletionIdentity) (tools.AgentMessageResult, error) {
	dir, ok := s.sidx.Lookup(target)
	if !ok {
		return tools.AgentMessageResult{}, errSessionNotFound
	}
	id, err := idgen.NewV7()
	if err != nil {
		return tools.AgentMessageResult{}, err
	}
	if completion != nil {
		id = fmt.Sprintf("%s:%d", completion.TaskID, completion.Generation)
	}
	message := types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: text}}, Origin: origin, ClientRequestID: id, Completion: completion, ContextOnly: true, Timestamp: time.Now().UnixMilli()}
	gate := s.inputGate(target)
	gate.Lock()
	defer gate.Unlock()
	_, err = session.EnqueueContext(dir, session.ContextQueuedItem{Message: message, IdempotencyKey: id})
	if err != nil {
		return tools.AgentMessageResult{}, err
	}
	status := "stored"
	if live := s.runAt(target); live != nil && s.pushSteerRun(live, steerRequest{Content: message.Content, Origin: origin, ClientRequestID: id, Completion: completion, ContextOnly: true}) {
		status = "queued"
	}
	s.publishQueueChanged(target)
	return tools.AgentMessageResult{AgentID: target, Status: status, Message: "message durably accepted"}, nil
}
func (s *Server) dispatchAgentMessage(ctx context.Context, req tools.AgentMessageRequest) (tools.AgentMessageResult, error) {
	if req.SenderSessionID == "" {
		return tools.AgentMessageResult{}, errAgentSenderUnknown
	}
	if _, ok := s.sidx.Lookup(req.SenderSessionID); !ok {
		return tools.AgentMessageResult{}, errSessionNotFound
	}
	if err := ctx.Err(); err != nil {
		return tools.AgentMessageResult{}, err
	}
	if strings.TrimSpace(req.Message) == "" {
		return tools.AgentMessageResult{}, fmt.Errorf("message is required")
	}
	snap, path, err := s.agentTasks.Resolve(req.SenderSessionID, req.Target)
	if err != nil {
		return tools.AgentMessageResult{}, err
	}
	_, senderPath, err := s.agentTasks.Identity(req.SenderSessionID)
	if err != nil {
		return tools.AgentMessageResult{}, err
	}
	if !req.TriggerTurn {
		result, err := s.acceptAgentContext(snap.SessionID, "agent:"+senderPath, req.Message, nil)
		if snap.TaskID != "" {
			result.AgentID = snap.TaskID
		}
		result.TaskName = path
		return result, err
	}
	if path == "/root" {
		return tools.AgentMessageResult{}, fmt.Errorf("followup_task cannot target root")
	}
	status, err := s.agentTasks.QueueOrResume(ctx, snap.TaskID, req.Message)
	return tools.AgentMessageResult{AgentID: snap.TaskID, TaskName: path, Status: status, Message: "task accepted"}, err
}
