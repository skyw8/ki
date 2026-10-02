package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

func (c *Controller) SetMaxConcurrent(limit int) {
	if limit < 1 {
		limit = 4
	}
	c.executionMu.Lock()
	c.maxConcurrent = limit
	c.executionMu.Unlock()
}

func (c *Controller) ReservePath(root, path string) (func(), error) {
	key := root + "\x00" + path
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errTaskStoreClosed
	}
	if c.blockedLocked(root, path) {
		return nil, fmt.Errorf("agent subtree is stopping")
	}
	if c.reservations[key] != nil {
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
	done := make(chan struct{})
	c.reservations[key] = done
	return sync.OnceFunc(func() {
		c.mu.Lock()
		delete(c.reservations, key)
		close(done)
		c.mu.Unlock()
	}), nil
}

func (c *Controller) SetIdentity(id, name, path, root string) error {
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
func (c *Controller) Identity(sessionID string) (root, path string, err error) {
	return c.identity(sessionID, map[string]bool{})
}

func (c *Controller) identity(sessionID string, seen map[string]bool) (string, string, error) {
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

func ResolvePath(caller, target string) (string, error) {
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

func (c *Controller) Resolve(callerSessionID, target string) (Snapshot, string, error) {
	root, caller, err := c.Identity(callerSessionID)
	if err != nil {
		return Snapshot{}, "", err
	}
	path, err := ResolvePath(caller, target)
	if err != nil {
		return Snapshot{}, "", err
	}
	if path == "/root" {
		return Snapshot{SessionID: root, RootSessionID: root, TaskPath: path}, path, nil
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
	return Snapshot{}, path, fmt.Errorf("unknown agent target %s", path)
}

func (c *Controller) Views(callerSessionID, prefix string) ([]View, error) {
	root, caller, err := c.Identity(callerSessionID)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		prefix = "/root"
	} else {
		prefix, err = ResolvePath(caller, prefix)
		if err != nil {
			return nil, err
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []View{}
	for _, task := range c.tasks {
		task.mu.Lock()
		snap := task.snap
		task.mu.Unlock()
		if snap.RootSessionID == root && (snap.TaskPath == prefix || strings.HasPrefix(snap.TaskPath, prefix+"/")) {
			out = append(out, ViewSnapshot(snap))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskName < out[j].TaskName })
	return out, nil
}

func ViewSnapshot(s Snapshot) View {
	return View{Progress: s.Progress, TaskName: s.TaskPath, SessionID: s.SessionID, AgentID: s.TaskID, Generation: s.Generation, Status: s.Status, Description: s.Description, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt, ToolUseCount: s.ToolUseCount, TotalTokens: s.TotalTokens}
}

// BlockSubtreeAdmission fences both reserved spawns and explicit follow-ups while
// host cleanup stops existing descendants. The returned release is idempotent.
func (c *Controller) BlockSubtreeAdmission(sessionID string) (func(), error) {
	release, _, err := c.FenceSubtreeAdmission(sessionID)
	return release, err
}

// FenceSubtreeAdmission also observes already-reserved spawns. Their release
// occurs after host creation or rollback, so drained is safe for tree deletion.
func (c *Controller) FenceSubtreeAdmission(sessionID string) (release func(), drained <-chan struct{}, err error) {
	root, path, err := c.Identity(sessionID)
	if err != nil {
		return nil, nil, err
	}
	key := root + "\x00" + path
	c.mu.Lock()
	c.blocked[key]++
	reservations := []<-chan struct{}{}
	for reservation, done := range c.reservations {
		reservedRoot, reservedPath, _ := strings.Cut(reservation, "\x00")
		if reservedRoot == root && (reservedPath == path || strings.HasPrefix(reservedPath, path+"/")) {
			reservations = append(reservations, done)
		}
	}
	c.mu.Unlock()
	done := make(chan struct{})
	if len(reservations) == 0 {
		close(done)
	} else {
		go func() {
			for _, reservation := range reservations {
				<-reservation
			}
			close(done)
		}()
	}
	return sync.OnceFunc(func() {
		c.mu.Lock()
		c.blocked[key]--
		if c.blocked[key] == 0 {
			delete(c.blocked, key)
		}
		c.mu.Unlock()
	}), done, nil
}

func (c *Controller) blockedLocked(root, path string) bool {
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
