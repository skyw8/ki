package process

// Identity attributes a process to its owning run and tool call. It is
// supplied explicitly so the process runtime stays independent of the loop.
type Identity struct {
	RunID, CallID, AgentID string
	Generation             uint64
}
