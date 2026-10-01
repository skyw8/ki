package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ki/internal/agent"
	"ki/internal/idgen"
	"ki/internal/session"
	"ki/internal/types"
)

func (s *Server) WaitAgent(ctx context.Context, caller string, timeout time.Duration) (agent.WaitResult, error) {
	st := s.runAt(caller)
	if st == nil {
		return agent.WaitResult{}, fmt.Errorf("wait_agent requires an active caller turn")
	}
	st.mu.Lock()
	inbox := st.inbox
	st.mu.Unlock()
	if inbox == nil {
		return agent.WaitResult{}, fmt.Errorf("caller mailbox unavailable")
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
	return agent.WaitResult{WakeReason: reason, TimedOut: timedOut, Message: message}, err
}
func (s *Server) ListAgents(caller, prefix string) ([]agent.View, error) {
	views, err := s.agentTasks.Views(caller, prefix)
	if err != nil {
		return nil, err
	}
	if prefix == "" || prefix == "/root" {
		root, _, err := s.agentTasks.Identity(caller)
		if err != nil {
			return nil, err
		}
		status := agent.Status("idle")
		if s.running(root) {
			status = agent.Running
		}
		views = append([]agent.View{{TaskName: "/root", SessionID: root, Status: status}}, views...)
	}
	return views, nil
}
func (s *Server) InterruptAgent(ctx context.Context, caller, target string) (agent.View, error) {
	if err := ctx.Err(); err != nil {
		return agent.View{}, err
	}
	snap, path, err := s.agentTasks.Resolve(caller, target)
	if err != nil {
		return agent.View{}, err
	}
	if path == "/root" || snap.SessionID == caller {
		return agent.View{}, fmt.Errorf("cannot interrupt root or self")
	}
	previous := agent.ViewSnapshot(snap)
	if snap.Status == agent.Running || snap.Status == agent.Pending {
		_, err = s.agentTasks.Stop(snap.TaskID)
	}
	return previous, err
}

// acceptAgentContext stores QueueOnly input before exposing a live wakeup.
func (s *Server) acceptAgentContext(target, origin, text string, completion *types.CompletionIdentity) (agent.MessageResult, error) {
	dir, ok := s.sidx.Lookup(target)
	if !ok {
		return agent.MessageResult{}, errSessionNotFound
	}
	id, err := idgen.NewV7()
	if err != nil {
		return agent.MessageResult{}, err
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
		return agent.MessageResult{}, err
	}
	status := "stored"
	if live := s.runAt(target); live != nil && s.pushSteerRun(live, steerRequest{Content: message.Content, Origin: origin, ClientRequestID: id, Completion: completion, ContextOnly: true}) {
		status = "queued"
	}
	s.publishQueueChanged(target)
	return agent.MessageResult{AgentID: target, Status: status, Message: "message durably accepted"}, nil
}
func (s *Server) dispatchAgentMessage(ctx context.Context, req agent.MessageRequest) (agent.MessageResult, error) {
	if req.SenderSessionID == "" {
		return agent.MessageResult{}, errAgentSenderUnknown
	}
	if _, ok := s.sidx.Lookup(req.SenderSessionID); !ok {
		return agent.MessageResult{}, errSessionNotFound
	}
	if err := ctx.Err(); err != nil {
		return agent.MessageResult{}, err
	}
	if strings.TrimSpace(req.Message) == "" {
		return agent.MessageResult{}, fmt.Errorf("message is required")
	}
	snap, path, err := s.agentTasks.Resolve(req.SenderSessionID, req.Target)
	if err != nil {
		return agent.MessageResult{}, err
	}
	_, senderPath, err := s.agentTasks.Identity(req.SenderSessionID)
	if err != nil {
		return agent.MessageResult{}, err
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
		return agent.MessageResult{}, fmt.Errorf("followup_task cannot target root")
	}
	status, err := s.agentTasks.QueueOrResume(ctx, snap.TaskID, req.Message)
	return agent.MessageResult{AgentID: snap.TaskID, TaskName: path, Status: status, Message: "task accepted"}, err
}
