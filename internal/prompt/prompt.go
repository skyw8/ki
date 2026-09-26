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
//
// A subagent's orientation (identity, depth, reply address) is not part of the
// system prompt: it travels in the child's first user message instead, so the
// child's prompt stays byte-identical to its parent's for prefix caching. See
// server.subagentDirective.
type Input struct {
	Resources resources.Snapshot
	Tools     []loop.Tool
	Toggle    session.Toggle
}

// DefaultAppendSystemPrompt is the built-in supplement rendered in the
// appended-system-prompt position for every session. Search-tool preferences
// belong to the harness rather than to one shell, so keeping them here avoids
// repeating the same paragraph in both the Bash and PowerShell descriptions
// (and keeps them when a model has no shell tool at all).
const DefaultAppendSystemPrompt = `IMPORTANT: Prefer Read, Grep, and Glob over shell equivalents (cat, head, sed, awk, echo).

NEVER use 'grep' or 'find' in shell commands or pipelines. ALWAYS use 'rg' and 'fd' instead — ki bundles both on PATH and they are the only supported search tools. 'fd' respects .gitignore and skips hidden files (-H shows hidden, -I disables ignore rules).`

// AppendSection returns the appended-system-prompt stack as one text, in the
// order Build renders it: the built-in supplement, the global file, the project
// file, then each enabled extension layer. The settings API serves it so the
// editor can preview exactly what the model receives without rebuilding a whole
// system prompt (which would need this session's tools and skills).
func AppendSection(res resources.Snapshot) string {
	var b strings.Builder
	for i, block := range appendBlocks(res) {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(block)
	}
	return b.String()
}

// appendBlocks lists the append stack in render order, dropping empty layers so
// a blank file cannot leave stray blank lines in the prompt.
func appendBlocks(res resources.Snapshot) []string {
	blocks := make([]string, 0, len(res.AppendSystemPrompts)+len(res.ExtensionPrompts)+1)
	blocks = append(blocks, DefaultAppendSystemPrompt)
	for _, layer := range res.AppendSystemPrompts {
		if strings.TrimSpace(layer.Text) == "" {
			continue
		}
		blocks = append(blocks, layer.Text)
	}
	for _, layer := range res.ExtensionPrompts {
		if strings.TrimSpace(layer.Text) == "" {
			continue
		}
		blocks = append(blocks, fmt.Sprintf("<extension_instructions name=%q>\n%s\n</extension_instructions>\n", layer.ExtensionID, layer.Text))
	}
	return blocks
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
	// The append stack sits after Ki's built-in guidance and before task-scoped
	// resources: the built-in supplement leads (so operator text adds to the
	// harness rules instead of replacing them) and every operator layer follows
	// in source order.
	for _, block := range appendBlocks(in.Resources) {
		b.WriteString("\n\n")
		b.WriteString(block)
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
