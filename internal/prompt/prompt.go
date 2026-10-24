package prompt

import (
	"fmt"
	"strings"

	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/skills"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	questiontools "ki/internal/tool/builtin/question"
)

type Input struct {
	Resources resources.Snapshot
	Tools     []toolapi.Tool
	Toggle    session.Toggle
}

const DefaultAppendSystemPrompt = `IMPORTANT: Prefer read, grep, and glob over shell equivalents (cat, head, sed, awk, echo).

In shell commands or pipelines, prefer 'rg' and 'fd' over 'grep' and 'find'. Ki bundles rg and fd on supported targets and puts them on PATH; on an unsupported target, use an available platform search command rather than assuming either binary exists. 'fd' respects .gitignore and skips hidden files (-H shows hidden, -I disables ignore).`

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

func Build(in Input) string {
	var b strings.Builder
	b.WriteString("You are a helpful assistant operating inside ki, a agent harness. You help users by reading files, executing commands, editing code, and writing new files.\n\n")
	env := in.Resources.Environment
	if env.KIHome != "" {
		fmt.Fprintf(&b, "Ki configuration (KI_HOME: %s, default ~/.ki): ki.toml = server/compaction/log, skills/ = SKILL.md packages, models.json + credentials.json = providers; project overrides in <cwd>/.ki/; `ki config path` prints the locations.\n\n", env.KIHome)
	}
	b.WriteString("Available tools:\n")
	hasRead := false
	hasQuestions := false
	if len(in.Tools) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, t := range in.Tools {
			if toolapi.Equal("read", t.Name()) {
				hasRead = true
			}
			if toolapi.Equal(catalog.RequestUserInputAsync, t.Name()) {
				hasQuestions = true
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
	if hasQuestions {
		b.WriteString("\n")
		b.WriteString(questiontools.Guidance)
	}
	for _, block := range appendBlocks(in.Resources) {
		b.WriteString("\n\n")
		b.WriteString(block)
	}

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
