package extension

// Kind is a top-level capability declared in extension.json.
type Kind string

const (
	CapPromptAppend Kind = "prompt.append"
	CapSkill        Kind = "skill"
	CapCommand      Kind = "command"
	CapTool         Kind = "tool"
	CapLifecycle    Kind = "lifecycle"
	CapBus          Kind = "bus"
	CapProvider     Kind = "provider"
)

var knownKinds = map[Kind]bool{
	CapPromptAppend: true,
	CapSkill:        true,
	CapCommand:      true,
	CapTool:         true,
	CapLifecycle:    true,
	CapBus:          true,
	CapProvider:     true,
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
	return hasKind(caps, CapTool) || hasKind(caps, CapLifecycle) || hasKind(caps, CapBus) || hasKind(caps, CapProvider)
}
