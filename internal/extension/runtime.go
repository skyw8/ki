package extension

import (
	"context"
	"time"
)

const runtimeStableUptime = 30 * time.Second

// runtimeRetry limits repeated post-initialize crashes, not ordinary reloads.
type runtimeRetry struct {
	fastExits int
}

func (r *runtimeRetry) afterExit(uptime time.Duration) time.Duration {
	if uptime >= runtimeStableUptime {
		r.fastExits = 0
		return 0
	}
	r.fastExits++
	if r.fastExits < 2 {
		return 0
	}
	// Saturate the counter too: a broken extension can run for months.
	if r.fastExits > 7 {
		r.fastExits = 7
	}
	return min(time.Second<<uint(r.fastExits-2), 30*time.Second)
}

type runtimeHooks struct {
	start func(context.Context, string, Descriptor) *rpcClient
	now   func() time.Time
	wait  func(context.Context, <-chan struct{}, time.Duration) bool
}

func defaultRuntimeHooks(m *Manager) runtimeHooks {
	return runtimeHooks{start: m.ensure, now: time.Now, wait: waitRuntimeChange}
}

func waitRuntimeChange(ctx context.Context, changed <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-changed:
	case <-timer.C:
	}
	return true
}

func sameRuntimes(a, b map[string]Descriptor) bool {
	if len(a) != len(b) {
		return false
	}
	for name, d := range a {
		other, ok := b[name]
		if !ok || !sameRuntime(d, other) {
			return false
		}
	}
	return true
}

// A session can discover an installed package before the next global reload.
// Admit only new names; a stale snapshot must not resurrect a name the
// authoritative catalog already removed or disabled.
func (m *Manager) adoptSnapshotLocked(d Descriptor) {
	if !m.configured && !m.closed {
		m.known[d.Name] = true
		return
	}
	if m.configured && !m.closed && !m.known[d.Name] && d.wantsSidecar() {
		m.known[d.Name] = true
		m.adopted[d.Name] = d
	}
}

// Direct in-process Prepare users need not configure a global catalog first.
// Configured managers also admit freshly discovered snapshot packages, but
// Configure supersedes their provisional admission before publication.
func (m *Manager) desiredLocked(d Descriptor) bool {
	if m.closed {
		return false
	}
	if !m.configured {
		return true
	}
	desired, ok := m.descs[d.Name]
	if !ok {
		desired, ok = m.adopted[d.Name]
	}
	return ok && sameRuntime(desired, d)
}

func (m *Manager) watchRuntime(ctx context.Context, d Descriptor, done chan struct{}, hooks runtimeHooks) {
	defer func() {
		m.mu.Lock()
		if m.watching[d.Name] == done {
			delete(m.watching, d.Name)
		}
		m.mu.Unlock()
		close(done)
	}()
	var retry runtimeRetry
	var previous Descriptor
	for {
		m.mu.Lock()
		desired, ok := m.descs[d.Name]
		changed := m.changed
		if !ok || ctx.Err() != nil {
			// Remove ownership under the same lock as the desired lookup so a
			// concurrent re-enable can install its new watcher without a gap.
			if m.watching[d.Name] == done {
				delete(m.watching, d.Name)
			}
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		d = desired
		if !sameRuntime(previous, d) {
			retry = runtimeRetry{}
		}
		previous = d
		c := hooks.start(ctx, "", d)
		if c == nil {
			m.setRuntimeStatus(d, "failed", "sidecar failed to start")
			if !m.waitRuntimeRetry(ctx, d, changed, 2*time.Second, hooks) {
				return
			}
			continue
		}
		started := hooks.now()
		m.setRuntimeStatus(d, "ready", "")
		reloaded := false
	wait:
		for {
			select {
			case <-ctx.Done():
				c.close()
				return
			case <-c.closed:
				break wait
			case <-changed:
				m.mu.Lock()
				next, exists := m.descs[d.Name]
				changed = m.changed
				m.mu.Unlock()
				if !exists || !sameRuntime(next, d) {
					reloaded = true
					break wait
				}
			}
		}
		m.mu.Lock()
		if current := m.by[d.Name]; current == c {
			delete(m.by, d.Name)
			for sessionID, opened := range m.sessionOpen {
				delete(opened, d.Name)
				if len(opened) == 0 {
					delete(m.sessionOpen, sessionID)
				}
			}
		}
		m.mu.Unlock()
		c.close()
		m.setRuntimeStatus(d, "restarting", "sidecar exited")
		// A ready-then-crash loop previously spawned without a pause and
		// flooded status pushes. First retry is prompt; repeated fast exits
		// back off. Explicit reloads must never wait on the old crash history.
		delay := time.Duration(0)
		if !reloaded {
			delay = retry.afterExit(hooks.now().Sub(started))
		}
		if !m.waitRuntimeRetry(ctx, d, changed, delay, hooks) {
			return
		}
	}
}

func (m *Manager) waitRuntimeRetry(ctx context.Context, d Descriptor, changed <-chan struct{}, delay time.Duration, hooks runtimeHooks) bool {
	deadline := hooks.now().Add(delay)
	for delay > 0 {
		m.mu.Lock()
		desired, ok := m.descs[d.Name]
		m.mu.Unlock()
		if !ok || !sameRuntime(desired, d) {
			return ctx.Err() == nil
		}
		if !hooks.wait(ctx, changed, delay) {
			return false
		}
		m.mu.Lock()
		desired, ok = m.descs[d.Name]
		changed = m.changed
		m.mu.Unlock()
		if !ok || !sameRuntime(desired, d) {
			return ctx.Err() == nil
		}
		// Unrelated catalog changes wake every watcher but must not bypass
		// this extension's remaining crash backoff.
		delay = deadline.Sub(hooks.now())
	}
	return ctx.Err() == nil
}
