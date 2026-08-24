package prompt

import (
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/loop"
	"ki/internal/resources"
	"ki/internal/skills"
	"ki/internal/tools"
)

func TestBuildLayers(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	snapshot := resources.Snapshot{
		Environment: resources.Environment{
			KIHome:       filepath.ToSlash(home),
			CWD:          filepath.ToSlash(cwd),
			OS:           "Linux",
			Architecture: "amd64",
			Date:         "2026-08-15",
			Timezone:     "UTC (UTC+0)",
		},
		AppendSystemPrompt: "extra operator instructions",
		ContextFiles:       []resources.ContextFile{{Path: filepath.Join(cwd, "AGENTS.md"), Content: "use tabs"}},
		Skills:             []skills.Skill{{Name: "demo", Description: "do demo", FilePath: filepath.Join(home, "skills", "demo", "SKILL.md")}},
	}
	sys := Build(Input{
		Resources: snapshot,
		Tools:     tools.Set{CWD: cwd}.Build(tools.Profile{RichRead: true, Editor: tools.EditorWriteEdit}),
	})
	if !strings.Contains(sys, "operating inside ki") {
		t.Fatalf("identity: %s", sys[:80])
	}
	if !strings.Contains(sys, "- Read:") || !strings.Contains(sys, "- Bash:") {
		t.Fatalf("snippets: %s", sys)
	}
	if strings.Contains(sys, "cat -n") {
		t.Fatal("long CC prompt leaked into system")
	}
	if !strings.Contains(sys, "extra operator instructions") {
		t.Fatal("append system prompt")
	}
	if appendAt, skillsAt := strings.Index(sys, "extra operator instructions"), strings.Index(sys, "<available_skills>"); appendAt < 0 || skillsAt < 0 || appendAt >= skillsAt {
		t.Fatalf("append system prompt position: append=%d skills=%d", appendAt, skillsAt)
	}
	if !strings.Contains(sys, "<available_skills>") || !strings.Contains(sys, "<name>demo</name>") {
		t.Fatalf("skills: %s", sys)
	}
	if !strings.Contains(sys, "use tabs") {
		t.Fatal("agents.md")
	}
	if !strings.Contains(sys, "Ki configuration") || !strings.Contains(sys, filepath.ToSlash(home)) {
		t.Fatalf("config layout: %s", sys)
	}
	if !strings.Contains(sys, "Current date: 2026-08-15") {
		t.Fatalf("date: %s", sys)
	}
	if !strings.Contains(sys, "Runtime environment:") || !strings.Contains(sys, "Architecture:") {
		t.Fatalf("runtime environment: %s", sys)
	}
}

func TestBuildNoSkillsWithoutRead(t *testing.T) {
	sys := Build(Input{Tools: []loop.Tool{}})
	if strings.Contains(sys, "<available_skills>") {
		t.Fatal("skills without Read")
	}
}
