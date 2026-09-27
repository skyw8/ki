package server

import (
	"ki/internal/loop"
	"ki/internal/session"
)

type snapshotReplay struct {
	entries map[string]bool
	tools   map[string]bool
	through int64
}

func (s *Server) replaySnapshot(id, leaf string) snapshotReplay {
	var out snapshotReplay
	if leaf == "" {
		return out
	}
	snap, err := s.loadSessionSnap(id, true, 0)
	if err != nil || !entryIn(snap.entries, leaf) {
		return out
	}
	out.entries, out.tools = map[string]bool{}, map[string]bool{}
	for _, e := range session.LeafChain(snap.entries, leaf) {
		out.entries[e.ID] = true
		if e.Message != nil && e.Message.Role == "toolResult" {
			out.tools[e.Message.ToolCallID] = true
		}
	}
	return out
}

// observe is called under the run lock before registering the reader. The
// snapshot names persisted entries, so work completed after the GET remains
// live even when it was buffered before the SSE connection opened.
func (s *snapshotReplay) observe(ev *loop.Event) {
	if ev.Type == loop.MessageEnd && s.entries[ev.EntryID] {
		s.through = max(s.through, ev.Seq)
	}
}

func (s snapshotReplay) covers(ev *loop.Event) bool {
	// Compact views intentionally lack hidden node IDs. Replaying their full
	// bodies would both download the fold and inflate its count. Filter on the
	// server using the snapshot's persisted branch, not client-visible nodes.
	if ev.EntryID != "" && s.entries[ev.EntryID] {
		return true
	}
	switch ev.Type {
	case loop.MessageStart, loop.MessageUpdate, loop.RequestHeader, loop.CompactionStart, loop.CompactionEnd:
		return s.through > 0 && ev.Seq <= s.through
	case loop.ToolExecutionStart, loop.ToolExecutionUpdate, loop.ToolExecutionEnd:
		// A sibling tool can finish while another is still running. A sequence
		// cutoff alone would lose that running tool's start/arguments.
		return ev.ToolCallID != "" && s.tools[ev.ToolCallID]
	}
	return false
}

func (s snapshotReplay) frame(ev loop.Event) loop.Event {
	if s.entries != nil && (ev.Type == loop.AgentEnd || ev.Type == loop.TurnEnd) {
		// These lifecycle frames repeat message_end bodies, including every
		// hidden reply at agent_end. The compact client needs only the boundary.
		ev.Message, ev.Messages, ev.ToolResults = nil, nil, nil
	}
	return ev
}
