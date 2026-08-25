package extension

// Kind is a top-level capability declared in extension.json.
type Kind string

const (
	CapPromptAppend Kind = "prompt.append"
	CapSkill        Kind = "skill"
	CapCommand      Kind = "command"
	CapMCP          Kind = "mcp"
	CapTool         Kind = "tool"
	CapLifecycle    Kind = "lifecycle"
	CapBus          Kind = "bus"
)

var knownKinds = map[Kind]bool{
	CapPromptAppend: true,
	CapSkill:        true,
	CapCommand:      true,
	CapMCP:          true,
	CapTool:         true,
	CapLifecycle:    true,
	CapBus:          true,
}

func hasKind(list []string, k Kind) bool {
	for _, s := range list {
		if s == string(k) {
			return true
		}
	}
	return false
}

func needsCodeRuntime(caps []string) bool {
	return hasKind(caps, CapTool) || hasKind(caps, CapLifecycle) || hasKind(caps, CapBus)
}
