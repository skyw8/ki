package extension

import "slices"

// Kind is a top-level capability declared in extension.json.
type Kind string

// Capability kinds declared in extension.json.
const (
	CapPromptAppend Kind = "prompt.append"
	CapSkill        Kind = "skill"
	CapCommand      Kind = "command"
	CapTool         Kind = "tool"
	CapLifecycle    Kind = "lifecycle"
	CapBus          Kind = "bus"
	CapProvider     Kind = "provider"
	CapChannel      Kind = "channel"
	CapSettings     Kind = "settings"
)

var knownKinds = map[Kind]bool{
	CapPromptAppend: true,
	CapSkill:        true,
	CapCommand:      true,
	CapTool:         true,
	CapLifecycle:    true,
	CapBus:          true,
	CapProvider:     true,
	CapChannel:      true,
	CapSettings:     true,
}

func hasKind(list []string, k Kind) bool {
	return slices.Contains(list, string(k))
}

func needsCodeRuntime(caps []string) bool {
	return hasKind(caps, CapTool) || hasKind(caps, CapLifecycle) || hasKind(caps, CapBus) || hasKind(caps, CapProvider) || hasKind(caps, CapChannel) || hasKind(caps, CapSettings)
}
