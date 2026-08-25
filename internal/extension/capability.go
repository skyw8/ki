package extension

// Kind is a top-level capability declared in extension.json.
type Kind string

const (
	CapPromptAppend Kind = "prompt.append"
	CapSkill        Kind = "skill"
	CapCommand      Kind = "command"
	CapMCP          Kind = "mcp"
	CapTool         Kind = "tool"
	CapHook         Kind = "hook"
	CapIntercept    Kind = "intercept"
)

const (
	InterceptTool         = "tool"
	InterceptProvider     = "provider"
	InterceptProviderHTTP = "provider.http"
)

var knownKinds = map[Kind]bool{
	CapPromptAppend: true,
	CapSkill:        true,
	CapCommand:      true,
	CapMCP:          true,
	CapTool:         true,
	CapHook:         true,
	CapIntercept:    true,
}

var knownIntercept = map[string]bool{
	InterceptTool:         true,
	InterceptProvider:     true,
	InterceptProviderHTTP: true,
}

func hasKind(list []string, k Kind) bool {
	for _, s := range list {
		if s == string(k) {
			return true
		}
	}
	return false
}

func hasPoint(list []string, point string) bool {
	for _, s := range list {
		if s == point {
			return true
		}
	}
	return false
}
