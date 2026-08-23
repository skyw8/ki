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
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Dir(root)
	_ = os.WriteFile(filepath.Join(outside, "AGENTS.md"), []byte("OUTSIDE"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT"), 0o600)
	nested := filepath.Join(root, "pkg")
	_ = os.MkdirAll(nested, 0o700)
	_ = os.WriteFile(filepath.Join(nested, "CLAUDE.md"), []byte("NEST"), 0o600)

	home := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("GLOBAL"), 0o600)

	files := CollectAgents(home, nested, "s1")
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
	_ = os.MkdirAll(filepath.Join(home, "skills", "demo"), 0o700)
	_ = os.WriteFile(filepath.Join(home, "skills", "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: do demo\n---\n# hi\n"), 0o600)
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("use tabs"), 0o600)
	sys, _ := Build(Input{
		Home:  home,
		CWD:   cwd,
		Tools: tools.Set{CWD: cwd}.Build(tools.Profile{RichRead: true, Editor: tools.EditorWriteEdit}),
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

func TestDetectOS(t *testing.T) {
	readFiles := func(files map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			value, ok := files[path]
			if !ok {
				return nil, os.ErrNotExist
			}
			return []byte(value), nil
		}
	}

	tests := []struct {
		name  string
		goos  string
		env   map[string]string
		files map[string]string
		want  string
	}{
		{name: "macOS", goos: "darwin", want: "macOS"},
		{name: "Windows", goos: "windows", want: "Windows"},
		{name: "Linux", goos: "linux", want: "Linux"},
		{
			name: "WSL environment marker",
			goos: "linux",
			env:  map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
			want: "WSL",
		},
		{
			name:  "WSL kernel marker",
			goos:  "linux",
			files: map[string]string{"/proc/sys/kernel/osrelease": "5.15.90.1-microsoft-standard-WSL2"},
			want:  "WSL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			if got := detectOSWith(tt.goos, getenv, readFiles(tt.files)); got != tt.want {
				t.Fatalf("detectOSWith() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildNoSkillsWithoutRead(t *testing.T) {
	sys, _ := Build(Input{Tools: []loop.Tool{}})
	if strings.Contains(sys, "<available_skills>") {
		t.Fatal("skills without Read")
	}
}

// TestAgentsCacheScopedPerSession pins the session-scoped cache contract:
// within one session a (home, cwd) pair is read once and pinned; a new
// session in the same workspace re-reads disk; InvalidateAgents re-reads
// only the target session.
func TestAgentsCacheScopedPerSession(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("GLOBAL"), 0o600)

	writeCwd := func(content string) {
		if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCwd("FIRST")
	s1 := "session-1"
	first := CollectAgents(home, cwd, s1)
	if !strings.Contains(first[0].Content, "GLOBAL") || !strings.Contains(first[1].Content, "FIRST") {
		t.Fatalf("initial: %+v", first)
	}

	// New content appears on disk; the same session must not pick it up.
	writeCwd("SECOND")
	again := CollectAgents(home, cwd, s1)
	if !strings.Contains(again[1].Content, "FIRST") {
		t.Fatalf("same-session cache not pinned: %+v", again)
	}

	// A new session in the same workspace re-reads disk.
	s2 := "session-2"
	fresh := CollectAgents(home, cwd, s2)
	if !strings.Contains(fresh[1].Content, "SECOND") {
		t.Fatalf("new session must re-read: %+v", fresh)
	}

	// InvalidateAgents forces only that session to re-read.
	writeCwd("THIRD")
	InvalidateAgents(home, cwd, s1)
	refreshed := CollectAgents(home, cwd, s1)
	if !strings.Contains(refreshed[1].Content, "THIRD") {
		t.Fatalf("after invalidate: %+v", refreshed)
	}
	if got := CollectAgents(home, cwd, s2); !strings.Contains(got[1].Content, "SECOND") {
		t.Fatalf("other session cache dropped: %+v", got)
	}

	// A different home has its own entry.
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(other, "AGENTS.md"), []byte("OTHER"), 0o600)
	o := CollectAgents(other, cwd, s1)
	if !strings.Contains(o[0].Content, "OTHER") {
		t.Fatalf("other home: %+v", o)
	}
}
