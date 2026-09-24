package command

import (
	"cmp"
	"os"
	"slices"
	"strings"

	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/skills"
)

// Item is one palette / GET commands[] row.
type Item struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	ArgumentHint string   `json:"argumentHint,omitempty"`
	Completions  []string `json:"completions,omitempty"`
	Source       string   `json:"source"`
	Extension    string   `json:"extension,omitempty"`
}

// Catalog lists builtins, prompt templates, and enabled skills.
func Catalog(snapshot resources.Snapshot, skillsToggle session.Toggle) []Item {
	out := []Item{
		{Name: "new", Description: "Start a new session", Source: "builtin"},
		{Name: "cd", Description: "Start a session in another directory", ArgumentHint: "<path>", Source: "builtin"},
		{Name: "compact", Description: "Compact this session's context", Source: "builtin"},
		{Name: "reload", Description: "Reload session resources and extensions", Source: "builtin"},
	}
	for _, t := range snapshot.Prompts {
		out = append(out, Item{
			Name: t.Name, Description: t.Description, ArgumentHint: t.ArgumentHint, Source: "prompt", Extension: t.Extension,
		})
	}
	for _, sk := range skills.Filter(snapshot.Skills, skillsToggle) {
		out = append(out, Item{
			Name: "skill:" + sk.Name, Description: sk.Description, Source: "skill",
		})
	}
	slices.SortStableFunc(out, func(a, b Item) int {
		if a.Source != b.Source {
			order := map[string]int{"builtin": 0, "prompt": 1, "extension": 2, "skill": 3}
			return cmp.Compare(order[a.Source], order[b.Source])
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

// ResolveUnknown upgrades KindUnknown to KindTemplate when a prompt file exists.
// User/home templates (Extension == "") win over extension markdown.
func ResolveUnknown(p Parsed, snapshot resources.Snapshot) Parsed {
	if p.Kind != KindUnknown {
		return p
	}
	if t, ok := templateByName(snapshot, p.Name); ok && t.Extension == "" {
		p.Kind = KindTemplate
		return p
	}
	if t, ok := templateByName(snapshot, p.Name); ok {
		p.Kind = KindTemplate
		_ = t
	}
	return p
}

// ResolveCommand classifies a slash against snapshot templates and runtime
// handlers. User/home prompt files beat extension CommandSpec.
func ResolveCommand(p Parsed, snapshot resources.Snapshot, runtimeNames map[string]struct{}) Parsed {
	if p.Kind != KindUnknown {
		return p
	}
	if t, ok := templateByName(snapshot, p.Name); ok && t.Extension == "" {
		p.Kind = KindTemplate
		return p
	}
	if _, ok := runtimeNames[p.Name]; ok {
		p.Kind = KindExtension
		return p
	}
	if _, ok := templateByName(snapshot, p.Name); ok {
		p.Kind = KindTemplate
	}
	return p
}

// ExpandSkill loads a SKILL.md body. Unknown or disabled names return false.
func ExpandSkill(snapshot resources.Snapshot, toggle session.Toggle, name, args string) (string, bool) {
	for _, sk := range skills.Filter(snapshot.Skills, toggle) {
		if sk.Name != name {
			continue
		}
		b, err := os.ReadFile(sk.FilePath)
		if err != nil {
			return "", false
		}
		body := stripFrontmatter(string(b))
		var sb strings.Builder
		sb.WriteString("<skill name=\"")
		sb.WriteString(sk.Name)
		sb.WriteString("\" location=\"")
		sb.WriteString(sk.FilePath)
		sb.WriteString("\">\n")
		sb.WriteString(strings.TrimSpace(body))
		sb.WriteString("\n</skill>")
		if strings.TrimSpace(args) != "" {
			sb.WriteString("\n\n")
			sb.WriteString(args)
		}
		return sb.String(), true
	}
	return "", false
}

func stripFrontmatter(text string) string {
	rest, ok := strings.CutPrefix(text, "---")
	if !ok {
		return text
	}
	_, body, found := strings.Cut(rest, "---")
	if !found {
		return text
	}
	return strings.TrimLeft(body, "\n")
}
