package process

type Identity struct {
	RunID, CallID, AgentID string
	Generation             uint64
}
