package server

import (
	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/tools"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// Runtime ownership outlives a turn, so these events also reach the global stream.
func (s *Server) publishRuntimeUpdate(id string, ev loop.Event) {
	dir, ok := s.sidx.Lookup(id)
	if !ok {
		return
	}
	// Runtime transitions must not advance a live writer's transcript leaf.
	entry, err := session.AppendSidebandEvent(dir, string(ev.Type), map[string]any{"process": ev.Process, "agent": ev.Agent})
	if err != nil {
		slog.Warn("persist runtime update", "session_id", id, "error", err)
		return
	}
	ev.EntryID = entry.ID
	ev.Timestamp = time.Now().UnixMilli()
	if live := s.runAt(id); live != nil {
		live.mu.Lock()
		live.appendLocked(&ev)
		live.wait.Broadcast()
		live.mu.Unlock()
	}
	s.publishPush(id, ev)
}
func (s *Server) processSnapshots(id string) []tools.ProcessSnapshot {
	s.mu.Lock()
	manager := s.processes[id]
	s.mu.Unlock()
	if manager == nil {
		return []tools.ProcessSnapshot{}
	}
	return manager.Snapshots()
}
func (s *Server) agentSnapshots(id string) []tools.AgentView {
	views, err := s.ListAgents(id, "")
	if err != nil {
		return []tools.AgentView{}
	}
	// Preserve active identities under a bounded GET even after many completed tasks.
	slices.SortStableFunc(views, func(a, b tools.AgentView) int {
		if a.TaskName == "/root" {
			return -1
		}
		if b.TaskName == "/root" {
			return 1
		}
		if a.Status == tools.TaskRunning && b.Status != tools.TaskRunning {
			return -1
		}
		if b.Status == tools.TaskRunning && a.Status != tools.TaskRunning {
			return 1
		}
		if a.LastActivityAt.After(b.LastActivityAt) {
			return -1
		}
		if b.LastActivityAt.After(a.LastActivityAt) {
			return 1
		}
		return strings.Compare(a.TaskName, b.TaskName)
	})
	if len(views) > 128 {
		views = views[:128]
	}
	return views
}
func (s *Server) stopRuntimeTree(id string) {
	// A descendant can spawn while the host traverses the tree. Fence admission
	// before collecting owners, then stop runners before collecting their processes.
	release, err := s.agentTasks.BlockSubtreeAdmission(id)
	if err != nil {
		return
	}
	defer release()
	views, _ := s.ListAgents(id, "")
	owned := []string{id}
	if snapshot, ok := s.agentTasks.TaskForSession(id); ok {
		_, _ = s.agentTasks.Stop(snapshot.TaskID)
	}
	for _, view := range views {
		if view.TaskName == "/root" || view.SessionID == id {
			continue
		}
		// Session-tree membership, not the caller's namespace, owns tree abort.
		if s.isSessionDescendant(view.SessionID, id) {
			owned = append(owned, view.SessionID)
			_, _ = s.agentTasks.Stop(view.AgentID)
			if live := s.runAt(view.SessionID); live != nil {
				s.cancelRun(view.SessionID, live, cancelReasonUserRequest, "tree", true)
			}
		}
	}
	for _, sessionID := range owned {
		if manager := s.existingProcesses(sessionID); manager != nil {
			manager.TerminateAll()
		}
	}
}
func (s *Server) existingProcesses(id string) *tools.ShellProcessManager {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processes[id]
}
func (s *Server) isSessionDescendant(child, parent string) bool {
	seen := map[string]bool{}
	for child != "" && !seen[child] {
		if child == parent {
			return true
		}
		seen[child] = true
		sess, err := s.open(child)
		if err != nil {
			return false
		}
		child = sess.Header.ParentSession
		_ = sess.Close()
	}
	return false
}
