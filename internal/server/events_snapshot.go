package server

import (
	"ki/internal/loop"
	"ki/internal/session"
)

type snapshotReplay struct {
	entries map[string]bool
	tools   map[string]bool
	users   map[string]bool
	through int64
}

func (s *Server) replaySnapshot(id, leaf string) snapshotReplay {
	var out snapshotReplay
	if leaf == "" {
		return out
	}
	snap, err := s.loadIndexedSessionSnap(id)
	if err != nil || !entryIn(snap.entries, leaf) {
		return out
	}
	out.entries, out.tools = map[string]bool{}, map[string]bool{}
	out.users = map[string]bool{}
	for _, e := range session.LeafChain(snap.entries, leaf) {
		out.entries[e.ID] = true
		if e.Message != nil && e.Message.Role == "user" && e.Message.ClientRequestID != "" {
			out.users[e.Message.ClientRequestID] = true
		}
		if e.Message != nil && e.Message.Role == "toolResult" {
			out.tools[e.Message.ToolCallID] = true
		}
	}
	return out
}

func (s *snapshotReplay) observe(ev *loop.Event) {
	if ev.Type == loop.MessageEnd && s.entries[ev.EntryID] {
		s.through = max(s.through, ev.Seq)
	}
}

func (s snapshotReplay) covers(ev *loop.Event) bool {
	if ev.EntryID != "" && s.entries[ev.EntryID] {
		return true
	}
	if (ev.Type == loop.SteerAccepted || ev.Type == loop.MessageStart) &&
		ev.Message != nil && ev.Message.Role == "user" {
		return ev.Message.ClientRequestID != "" && s.users[ev.Message.ClientRequestID]
	}
	switch ev.Type {
	case loop.SteerAccepted:
		return false
	case loop.MessageStart, loop.MessageUpdate, loop.RequestHeader, loop.CompactionStart, loop.CompactionEnd:
		return s.through > 0 && ev.Seq <= s.through
	case loop.ToolExecutionStart, loop.ToolExecutionUpdate, loop.ToolExecutionEnd, loop.PatchApplyUpdated:
		return ev.ToolCallID != "" && s.tools[ev.ToolCallID]
	}
	return false
}

func (s snapshotReplay) frame(ev loop.Event) loop.Event {
	if s.entries != nil && (ev.Type == loop.AgentEnd || ev.Type == loop.TurnEnd) {
		ev.Message, ev.Messages, ev.ToolResults = nil, nil, nil
	}
	return ev
}
