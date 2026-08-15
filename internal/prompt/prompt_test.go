package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/tools"
)

func TestCollectAgentsStopsAtGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Dir(root)
	_ = os.WriteFile(filepath.Join(outside, "AGENTS.md"), []byte("OUTSIDE"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT"), 0o644)
	nested := filepath.Join(root, "pkg")
	_ = os.MkdirAll(nested, 0o755)
	_ = os.WriteFile(filepath.Join(nested, "CLAUDE.md"), []byte("NEST"), 0o644)

	home := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("GLOBAL"), 0o644)

	files := CollectAgents(home, nested)
	var texts []string
	for _, f := range files {
		texts = append(texts, f.Content)
	}
	joined := strings.Join(texts, ",")
	if !strings.Contains(joined, "GLOBAL") || !strings.Contains(joined, "ROOT") || !strings.Contains(joined, "NEST") {
		t.Fatalf("missing expected: %v", texts)
	}
	if strings.Contains(joined, "OUTSIDE") {
		t.Fatalf("walked past git root: %v", texts)
	}
}

func TestBuildLayers(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, "skills", "demo"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "skills", "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: do demo\n---\n# hi\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("use tabs"), 0o644)
	sys, _ := Build(Input{
		Home:  home,
		CWD:   cwd,
		Tools: tools.Set{CWD: cwd}.All(),
		Now:   time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
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
	if !strings.Contains(sys, "<available_skills>") || !strings.Contains(sys, "<name>demo</name>") {
		t.Fatalf("skills: %s", sys)
	}
	if !strings.Contains(sys, "use tabs") {
		t.Fatal("agents.md")
	}
	if !strings.Contains(sys, "Current date: 2026-08-15") {
		t.Fatalf("date: %s", sys)
	}
}

func TestBuildNoSkillsWithoutRead(t *testing.T) {
	sys, _ := Build(Input{Tools: []loop.Tool{}})
	if strings.Contains(sys, "<available_skills>") {
		t.Fatal("skills without Read")
	}
}
