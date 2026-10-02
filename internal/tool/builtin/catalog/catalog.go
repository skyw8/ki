package catalog

import (
	"slices"

	toolapi "ki/internal/tool"
)

// Names returns all reserved built-in identifiers, including unavailable tools.
func Names() []string {
	return slices.Concat(FileNames(), ShellNames(), AgentNames(), CodeNames(), []string{SearchTool})
}
func FileNames() []string  { return []string{Read, Write, Edit, Grep, Glob, ApplyPatch} }
func ShellNames() []string { return []string{ExecCommand, WriteStdin} }
func CodeNames() []string  { return []string{CodeExec, CodeWait} }
func AgentNames() []string {
	return []string{SpawnAgent, SendMessage, FollowupTask, WaitAgent, InterruptAgent, ListAgents}
}
func IsReserved(name string) bool {
	for _, n := range Names() {
		if toolapi.Equal(name, n) {
			return true
		}
	}
	return false
}

const (
	Read           = "read"
	Write          = "write"
	Edit           = "edit"
	Grep           = "grep"
	Glob           = "glob"
	ApplyPatch     = "apply_patch"
	ExecCommand    = "exec_command"
	WriteStdin     = "write_stdin"
	SpawnAgent     = "spawn_agent"
	SendMessage    = "send_message"
	FollowupTask   = "followup_task"
	WaitAgent      = "wait_agent"
	InterruptAgent = "interrupt_agent"
	ListAgents     = "list_agents"
	CodeExec       = "exec"
	CodeWait       = "wait"
	SearchTool     = "search_tool"
)
