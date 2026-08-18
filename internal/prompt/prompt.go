package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/skills"
)

// Input is everything needed to assemble the system prompt.
type Input struct {
	Home   string
	CWD    string
	Tools  []loop.Tool
	Toggle session.Toggle
	Now    time.Time
}

// Build returns the layered system prompt (recomputed after compaction).
func Build(in Input) (string, []skills.Skill) {
	var b strings.Builder
	b.WriteString("You are an expert coding assistant operating inside ki, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.\n\n")
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

	// Toggle already dropped disabled names; this is listing, not a process.
	sk := skills.Discover(in.Home, in.CWD, in.Toggle)
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

	files := CollectAgents(in.Home, in.CWD)
	if len(files) > 0 {
		b.WriteString("\n\n<project_context>\n\nProject-specific instructions and guidelines:\n\n")
		for _, f := range files {
			fmt.Fprintf(&b, "<project_instructions path=%q>\n%s\n</project_instructions>\n\n", f.Path, f.Content)
		}
		b.WriteString("</project_context>\n")
	}

	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	_, offset := now.Zone()
	tz := now.Format("MST")
	fmt.Fprintf(&b, "\nCurrent working directory: %s\n", filepath.ToSlash(cwd))
	fmt.Fprintf(&b, "Current date: %s\nTimezone: %s (UTC%+d)\n", now.Format("2006-01-02"), tz, offset/3600)
	return b.String(), sk
}

// File is one AGENTS/CLAUDE context file.
type File struct {
	Path    string
	Content string
}

var candidates = []string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

// CollectAgents loads global then cwd→git root (inclusive).
func CollectAgents(home, cwd string) []File {
	var out []File
	seen := map[string]bool{}
	add := func(dir string) {
		f := loadOne(dir)
		if f == nil || seen[f.Path] {
			return
		}
		seen[f.Path] = true
		out = append(out, *f)
	}
	if home != "" {
		add(home)
	}
	if cwd == "" {
		return out
	}
	root := findGitRoot(cwd)
	var stack []string
	d := cwd
	for {
		stack = append(stack, d)
		if root == "" || same(d, root) || same(d, filepath.Dir(d)) {
			if root == "" {
				// not a repo: only cwd
				stack = []string{cwd}
			}
			break
		}
		p := filepath.Dir(d)
		if p == d {
			break
		}
		d = p
	}
	// global first, then root→cwd (stack is cwd→root)
	for _, v := range slices.Backward(stack) {
		add(v)
	}
	return out
}

func loadOne(dir string) *File {
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		//nolint:gosec // p was discovered under the configured prompt roots.
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return &File{Path: p, Content: string(b)}
	}
	return nil
}

func findGitRoot(cwd string) string {
	d := cwd
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			return ""
		}
		d = p
	}
}

func same(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return aa == bb
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
