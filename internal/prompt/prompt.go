package prompt

import (
	"fmt"
	"strings"

	"ki/internal/loop"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/skills"
)

// Input is everything needed to assemble the system prompt.
type Input struct {
	Resources resources.Snapshot
	Tools     []loop.Tool
	Toggle    session.Toggle
}

// Build renders a system prompt from an already loaded resource snapshot.
func Build(in Input) string {
	var b strings.Builder
	b.WriteString("You are a helpful assistant operating inside ki, a agent harness. You help users by reading files, executing commands, editing code, and writing new files.\n\n")
	// Ki self-configuration (the single-binary analogue of pi's docs section):
	// one short line the model reads when asked where to change server/skills
	// settings. Keep the list in sync with docs/*.md.
	env := in.Resources.Environment
	if env.KIHome != "" {
		fmt.Fprintf(&b, "Ki configuration (KI_HOME: %s, default ~/.ki): ki.toml = server/compaction/log, skills/ = SKILL.md packages, models.json + credentials.json = providers; project overrides in <cwd>/.ki/; `ki config path` prints the locations.\n\n", env.KIHome)
	}
	b.WriteString("Available tools:\n")
	hasRead := false
	if len(in.Tools) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, t := range in.Tools {
			if t.Name() == "Read" {
				hasRead = true
			}
			snip := t.Snippet()
			if snip == "" {
				snip = t.Description()
			}
			fmt.Fprintf(&b, "- %s: %s\n", t.Name(), snip)
		}
	}
	b.WriteString("\nIn addition to the tools above, you may have access to other custom tools depending on the project.\n\n")
	b.WriteString("Guidelines:\n- Be concise in your responses\n- Show file paths clearly when working with files\n")
	// Keep the supplement after Ki's built-in guidance and before task-scoped
	// resources. This gives operators a stable layer for extra global/project
	// instructions without allowing it to replace the base prompt.
	if in.Resources.AppendSystemPrompt != "" {
		b.WriteString("\n\n")
		b.WriteString(in.Resources.AppendSystemPrompt)
	}
	for _, layer := range in.Resources.ExtensionPrompts {
		if strings.TrimSpace(layer.Text) == "" {
			continue
		}
		fmt.Fprintf(&b, "\n\n<extension_instructions name=%q>\n%s\n</extension_instructions>\n", layer.ExtensionID, layer.Text)
	}

	// Toggle already dropped disabled names; this is listing, not a process.
	sk := skills.Filter(in.Resources.Skills, in.Toggle)
	if hasRead && len(sk) > 0 {
		b.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\n")
		b.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
		b.WriteString("When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n\n")
		b.WriteString("<available_skills>\n")
		for _, s := range sk {
			fmt.Fprintf(&b, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n  </skill>\n",
				xmlEscape(s.Name), xmlEscape(s.Description), xmlEscape(s.FilePath))
		}
		b.WriteString("</available_skills>\n")
	}

	files := in.Resources.ContextFiles
	if len(files) > 0 {
		b.WriteString("\n\n<project_context>\n\nProject-specific instructions and guidelines:\n\n")
		for _, f := range files {
			fmt.Fprintf(&b, "<project_instructions path=%q>\n%s\n</project_instructions>\n\n", f.Path, f.Content)
		}
		b.WriteString("</project_context>\n")
	}

	fmt.Fprintf(&b, "\nRuntime environment:\n- OS: %s\n- Architecture: %s\n", env.OS, env.Architecture)
	fmt.Fprintf(&b, "\nCurrent working directory: %s\n", env.CWD)
	fmt.Fprintf(&b, "Current date: %s\nTimezone: %s\n", env.Date, env.Timezone)
	return b.String()
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
