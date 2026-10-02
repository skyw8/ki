package extension

import (
	"context"
	"encoding/json"
	"slices"
)

type lifecycleNotification struct {
	SessionID string `json:"sessionId"`
	Event     string `json:"event"`
	Payload   Event  `json:"payload"`
}

// OnEvent fans out a redacted event to async subscribers.
func (m *Manager) OnEvent(_ context.Context, sessionID string, ev Event) {
	m.mu.Lock()
	var clients []*rpcClient
	for _, name := range m.order[sessionID] {
		c := m.by[name]
		if c != nil && c.hasAsync(ev.Type) && !slices.Contains(clients, c) {
			clients = append(clients, c)
		}
	}
	m.mu.Unlock()
	if len(clients) == 0 {
		return
	}
	// Streaming events can contain growing message snapshots. Encode the
	// immutable payload once, not once per subscriber; keep writes synchronous
	// so message_end cannot be overtaken by agent_settled.
	raw, err := json.Marshal(lifecycleNotification{SessionID: sessionID, Event: ev.Type, Payload: ev})
	if err != nil {
		return
	}
	for _, c := range clients {
		c.notifyRaw("lifecycle.event", raw)
	}
}
