package server

import (
	"log/slog"

	"ki/internal/session"
)

type sessionActivity struct {
	running     map[string]bool
	descendants map[string]int
}

// sessionActivity projects a whole list from one run snapshot. Rebuilding the
// ancestry graph per row would make sidebar refreshes quadratic.
func (s *Server) sessionActivity(infos []session.Info) sessionActivity {
	running := s.activeSessions()
	return sessionActivity{running: running, descendants: countActiveDescendants(infos, running)}
}

func (s *Server) activeSessions() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make(map[string]bool)
	for id, st := range s.runs {
		select {
		case <-st.done:
		default:
			active[id] = true
		}
	}
	return active
}

func (s *Server) activeDescendantCount(id string) int {
	active := s.activeSessions()
	// An idle server (or only this session running) needs no filesystem walk
	// for its detail view. A session is never its own active descendant.
	if len(active) == 0 || len(active) == 1 && active[id] {
		return 0
	}
	infos, err := s.slist.List(s.cfg.Sessions.Root)
	if err != nil {
		slog.Warn("project session activity", "session_id", id, "err", err)
		return 0
	}
	return countActiveDescendants(infos, active)[id]
}

// countActiveDescendants follows durable parentSession edges, independently of
// fork display mode and workspace. Each reachable active session counts once,
// excluding the session itself even when malformed ancestry contains a cycle.
func countActiveDescendants(infos []session.Info, active map[string]bool) map[string]int {
	type node struct {
		parent   string
		children int
		total    int
		own      int
	}
	nodes := make(map[string]*node, len(infos))
	for _, info := range infos {
		own := 0
		if active[info.ID] {
			own = 1
		}
		nodes[info.ID] = &node{parent: info.ParentSessionID, total: own, own: own}
	}
	for _, n := range nodes {
		if parent := nodes[n.parent]; parent != nil {
			parent.children++
		}
	}
	queue := make([]string, 0, len(nodes))
	for id, n := range nodes {
		if n.children == 0 {
			queue = append(queue, id)
		}
	}
	counts := make(map[string]int, len(nodes))
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		n := nodes[id]
		counts[id] = n.total - n.own
		if parent := nodes[n.parent]; parent != nil {
			parent.total += n.total
			parent.children--
			if parent.children == 0 {
				queue = append(queue, n.parent)
			}
		}
	}
	// Leaf removal leaves only cycles. Every member reaches the same cycle
	// and attached trees, so sum those totals once rather than walking every
	// ancestor chain. Missing parents simply terminate their own component.
	for id, n := range nodes {
		if n.children == 0 {
			continue
		}
		var cycle []string
		total := 0
		for cur := id; nodes[cur].children != 0; cur = nodes[cur].parent {
			member := nodes[cur]
			cycle = append(cycle, cur)
			total += member.total
			member.children = 0
		}
		for _, cur := range cycle {
			counts[cur] = total - nodes[cur].own
		}
	}
	return counts
}
