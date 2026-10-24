package command

import (
	"regexp"
	"strings"
)

type Kind int

const (
	KindPrompt Kind = iota
	KindBuiltin
	KindSkill
	KindTemplate
	KindUnknown
	KindExtension
)

type Parsed struct {
	Kind Kind
	Name string
	Args string
}

var (
	skillRe = regexp.MustCompile(`(?is)^/skill:([^\s]+)(?:\s+(.*))?$`)
	cmdRe   = regexp.MustCompile(`(?is)^/([A-Za-z][\w-]*)(?:\s+(.*))?$`)
)

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
		case "compact", "reload", "new", "cd":
			return Parsed{Kind: KindBuiltin, Name: name, Args: args}
		default:
			return Parsed{Kind: KindUnknown, Name: name, Args: args}
		}
	}
	return Parsed{Kind: KindPrompt}
}

func AllowBusy(p Parsed) bool {
	return p.Kind == KindBuiltin && p.Name == "reload"
}
