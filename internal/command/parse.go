package command

import (
	"regexp"
	"strings"
)

// Kind is how Parse classified the trimmed user text.
type Kind int

const (
	// KindPrompt is ordinary user text rather than a slash command.
	KindPrompt Kind = iota
	// KindBuiltin is a built-in slash command handled by the server.
	KindBuiltin
	// KindSkill is a discovered skill command.
	KindSkill
	// KindTemplate is a discovered prompt template command.
	KindTemplate
	// KindUnknown is a slash command with no matching resource.
	KindUnknown
	// KindExtension is a runtime handler registered by an extension sidecar.
	KindExtension
)

// Parsed is one slash (or a normal prompt).
type Parsed struct {
	Kind Kind
	Name string // compact, reload, review, or skill name without "skill:"
	Args string
}

var (
	skillRe = regexp.MustCompile(`(?is)^/skill:([^\s]+)(?:\s+(.*))?$`)
	cmdRe   = regexp.MustCompile(`(?is)^/([A-Za-z][\w-]*)(?:\s+(.*))?$`)
)

// Parse classifies trimmed text. Paths like /usr/bin stay KindPrompt.
func Parse(text string) Parsed {
	text = strings.TrimSpace(text)
	if text == "" || !strings.HasPrefix(text, "/") {
		return Parsed{Kind: KindPrompt}
	}
	if m := skillRe.FindStringSubmatch(text); m != nil {
		return Parsed{Kind: KindSkill, Name: m[1], Args: strings.TrimSpace(m[2])}
	}
	if m := cmdRe.FindStringSubmatch(text); m != nil {
		name := strings.ToLower(m[1])
		args := strings.TrimSpace(m[2])
		switch name {
		case "compact", "reload", "new", "cwd":
			return Parsed{Kind: KindBuiltin, Name: name, Args: args}
		default:
			return Parsed{Kind: KindUnknown, Name: name, Args: args}
		}
	}
	return Parsed{Kind: KindPrompt}
}

// AllowBusy reports whether the command may run while the session is occupied.
func AllowBusy(p Parsed) bool {
	return p.Kind == KindBuiltin && p.Name == "reload"
}
