package tools

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

func (c *AgentController) SetMaxConcurrent(limit int) {
	if limit < 1 {
		limit = 4
	}
	c.executionMu.Lock()
	c.maxConcurrent = limit
	c.executionMu.Unlock()
}
func (c *AgentController) ReservePath(root, path string) (func(), error) {
	key := root + "\x00" + path
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errTaskStoreClosed
	}
	if c.blockedLocked(root, path) {
		return nil, fmt.Errorf("agent subtree is stopping")
	}
	if c.reservations[key] {
		return nil, fmt.Errorf("task path %s already reserved", path)
	}
	for _, task := range c.tasks {
		task.mu.Lock()
		exists := task.snap.RootSessionID == root && task.snap.TaskPath == path
		task.mu.Unlock()
		if exists {
			return nil, fmt.Errorf("task path %s already exists; use followup_task", path)
		}
	}
	c.reservations[key] = true
	return func() { c.mu.Lock(); delete(c.reservations, key); c.mu.Unlock() }, nil
}
func (c *AgentController) SetIdentity(id, name, path, root string) error {
	task, ok := c.task(id)
	if !ok {
		return fmt.Errorf("unknown agent %s", id)
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	task.snap.TaskName = name
	task.snap.TaskPath = path
	task.snap.RootSessionID = root
	return task.persistLocked()
}

// Identity follows durable structural parents, allowing restoration in any order.
func (c *AgentController) Identity(sessionID string) (root, path string, err error) {
	return c.identity(sessionID, map[string]bool{})
}
func (c *AgentController) identity(sessionID string, seen map[string]bool) (string, string, error) {
	if seen[sessionID] {
		return "", "", fmt.Errorf("agent parent cycle at %s", sessionID)
	}
	seen[sessionID] = true
	task, ok := c.TaskForSession(sessionID)
	if !ok {
		return sessionID, "/root", nil
	}
	if task.RootSessionID != "" && task.TaskPath != "" {
		return task.RootSessionID, task.TaskPath, nil
	}
	root, parent, err := c.identity(task.ParentSessionID, seen)
	if err != nil {
		return "", "", err
	}
	name := task.TaskName
	if name == "" {
		name = "legacy_" + sessionID
	}
	path := parent + "/" + name
	if err = c.SetIdentity(task.TaskID, name, path, root); err != nil {
		return "", "", err
	}
	return root, path, nil
}
func ResolveAgentPath(caller, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("target is required")
	}
	path := target
	if !strings.HasPrefix(path, "/") {
		path = caller + "/" + path
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "" || parts[1] != "root" {
		return "", fmt.Errorf("invalid agent path %q", target)
	}
	for _, p := range parts[2:] {
		if err := ValidateTaskName(p); err != nil {
			return "", err
		}
	}
	return path, nil
}
func (c *AgentController) Resolve(callerSessionID, target string) (AgentSnapshot, string, error) {
	root, caller, err := c.Identity(callerSessionID)
	if err != nil {
		return AgentSnapshot{}, "", err
	}
	path, err := ResolveAgentPath(caller, target)
	if err != nil {
		return AgentSnapshot{}, "", err
	}
	if path == "/root" {
		return AgentSnapshot{SessionID: root, RootSessionID: root, TaskPath: path}, path, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, task := range c.tasks {
		task.mu.Lock()
		snap := task.snap
		task.mu.Unlock()
		if snap.RootSessionID == root && snap.TaskPath == path {
			return snap, path, nil
		}
	}
	return AgentSnapshot{}, path, fmt.Errorf("unknown agent target %s", path)
}
func (c *AgentController) Views(callerSessionID, prefix string) ([]AgentView, error) {
	root, caller, err := c.Identity(callerSessionID)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		prefix = "/root"
	} else {
		prefix, err = ResolveAgentPath(caller, prefix)
		if err != nil {
			return nil, err
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []AgentView{}
	for _, task := range c.tasks {
		task.mu.Lock()
		snap := task.snap
		task.mu.Unlock()
		if snap.RootSessionID == root && (snap.TaskPath == prefix || strings.HasPrefix(snap.TaskPath, prefix+"/")) {
			out = append(out, ViewAgent(snap))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskName < out[j].TaskName })
	return out, nil
}
func ViewAgent(s AgentSnapshot) AgentView {
	return AgentView{AgentProgress: s.AgentProgress, TaskName: s.TaskPath, SessionID: s.SessionID, AgentID: s.TaskID, Generation: s.Generation, Status: s.Status, Description: s.Description, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt, ToolUseCount: s.ToolUseCount, TotalTokens: s.TotalTokens}
}

// BlockSubtreeAdmission fences both reserved spawns and explicit follow-ups while
// host cleanup stops existing descendants. The returned release is idempotent.
func (c *AgentController) BlockSubtreeAdmission(sessionID string) (func(), error) {
	root, path, err := c.Identity(sessionID)
	if err != nil {
		return nil, err
	}
	key := root + "\x00" + path
	c.mu.Lock()
	c.blocked[key]++
	c.mu.Unlock()
	return sync.OnceFunc(func() {
		c.mu.Lock()
		c.blocked[key]--
		if c.blocked[key] == 0 {
			delete(c.blocked, key)
		}
		c.mu.Unlock()
	}), nil
}
func (c *AgentController) blockedLocked(root, path string) bool {
	if path == "" {
		path = "/root"
	}
	for key := range c.blocked {
		blockedRoot, prefix, _ := strings.Cut(key, "\x00")
		if blockedRoot == root && (path == prefix || strings.HasPrefix(path, prefix+"/")) {
			return true
		}
	}
	return false
}
