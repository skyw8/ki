package command

import (
	"os"
	"sort"
	"strings"

	"ki/internal/session"
	"ki/internal/skills"
)

// Item is one palette / GET commands[] row.
type Item struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	Source       string `json:"source"`
}

// Catalog lists builtins, prompt templates, and enabled skills.
func Catalog(home, cwd, sessionID string, skillsToggle session.Toggle) []Item {
	out := []Item{
		{Name: "compact", Description: "Compact this session's context", Source: "builtin"},
		{Name: "reload", Description: "Reload skills, prompts, AGENTS.md, and MCP config", Source: "builtin"},
	}
	for _, t := range listTemplates(home, cwd, sessionID) {
		out = append(out, Item{
			Name: t.Name, Description: t.Description, ArgumentHint: t.ArgumentHint, Source: "prompt",
		})
	}
	for _, sk := range skills.Discover(home, cwd, sessionID, skillsToggle) {
		out = append(out, Item{
			Name: "skill:" + sk.Name, Description: sk.Description, Source: "skill",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			order := map[string]int{"builtin": 0, "prompt": 1, "skill": 2}
			return order[out[i].Source] < order[out[j].Source]
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ResolveUnknown upgrades KindUnknown to KindTemplate when a prompt file exists.
func ResolveUnknown(p Parsed, home, cwd, sessionID string) Parsed {
	if p.Kind != KindUnknown {
		return p
	}
	if _, ok := templateByName(home, cwd, sessionID, p.Name); ok {
		p.Kind = KindTemplate
	}
	return p
}

// ExpandSkill loads a SKILL.md body. Unknown or disabled names return false.
func ExpandSkill(home, cwd, sessionID string, toggle session.Toggle, name, args string) (string, bool) {
	for _, sk := range skills.Discover(home, cwd, sessionID, toggle) {
		if sk.Name != name {
			continue
		}
		b, err := os.ReadFile(sk.FilePath) //nolint:gosec // path from skills.Discover
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
	if !strings.HasPrefix(text, "---") {
		return text
	}
	i := strings.Index(text[3:], "---")
	if i < 0 {
		return text
	}
	return strings.TrimLeft(text[3+i+3:], "\n")
}
