package skills

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ki/internal/session"
)

type discEnt struct {
	skills []Skill
}

var (
	discMu    sync.Mutex
	discCache = map[string]discEnt{}
)

// Skill is one discovered skill.
type Skill struct {
	Name        string
	Description string
	FilePath    string
	Source      string
}

type scanDir struct {
	path   string
	source string
}

// List discovers all skills without applying a session toggle.
func List(home, cwd, sessionID string) []Skill {
	return Discover(home, cwd, sessionID, session.Toggle{})
}

// Discover walks the standard skill directories and applies the session toggle.
func Discover(home, cwd, sessionID string, toggle session.Toggle) []Skill {
	var out []Skill
	for _, s := range listCached(home, cwd, sessionID) {
		if toggle.Allowed(s.Name) {
			out = append(out, s)
		}
	}
	return out
}

func listCached(home, cwd, sessionID string) []Skill {
	key := home + "\x00" + cwd + "\x00" + sessionID
	discMu.Lock()
	defer discMu.Unlock()
	// Cache-first, scoped per session: prompt.Build scans on every message;
	// keep that off the disk path except on a session's first scan (or after
	// invalidation). A new session — even in the same workspace — re-scans.
	if e, ok := discCache[key]; ok {
		return e.skills
	}
	all := scanAll(home, cwd)
	discCache[key] = discEnt{skills: all}
	return all
}

// Invalidate drops the cached scan for one session so the next Discover
// re-scans the skill directories. A future /reload command should call this
// before rebuilding the prompt.
func Invalidate(home, cwd, sessionID string) {
	key := home + "\x00" + cwd + "\x00" + sessionID
	discMu.Lock()
	defer discMu.Unlock()
	delete(discCache, key)
}

// InvalidateAll drops every cached scan. Useful for a global reload.
func InvalidateAll() {
	discMu.Lock()
	defer discMu.Unlock()
	discCache = map[string]discEnt{}
}

func scanAll(home, cwd string) []Skill {
	seen := map[string]bool{}
	var out []Skill
	for _, dir := range scanDirs(home, cwd) {
		walkSkillRoot(dir.path, func(path string) {
			s, err := load(path)
			if err != nil || s.Name == "" || seen[s.Name] {
				return
			}
			s.Source = dir.source
			seen[s.Name] = true
			out = append(out, s)
		})
	}
	return out
}

// walkSkillRoot visits SKILL.md in each top-level package (symlinks followed).
//
// Do not filepath.Walk the package: office skills ship large xsd/script trees,
// and that walk ran on every prompt. The model loads extra files via Read.
func walkSkillRoot(root string, visit func(path string)) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		info, err := os.Stat(p)
		if err != nil || info == nil {
			continue
		}
		if !info.IsDir() {
			if strings.EqualFold(info.Name(), "skill.md") {
				visit(p)
			}
			continue
		}
		dir := p
		// filepath.Walk does not follow a skill dir that is a symlink
		// ({KI_HOME}/skills/docx -> elsewhere).
		if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved != "" {
			dir = resolved
		}
		for _, name := range []string{"SKILL.md", "skill.md"} {
			md := filepath.Join(dir, name)
			st, err := os.Stat(md)
			if err == nil && st != nil && !st.IsDir() {
				visit(md)
				break
			}
		}
	}
}

func scanDirs(home, cwd string) []scanDir {
	var dirs []scanDir
	if home != "" {
		dirs = append(dirs, scanDir{filepath.Join(home, "skills"), "home"})
	}
	if user, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, scanDir{filepath.Join(user, ".agents", "skills"), "user-agents"})
	}
	if cwd != "" {
		dirs = append(dirs, scanDir{filepath.Join(cwd, ".ki", "skills"), "project"})
		// ancestors' .agents/skills up to git root (or cwd only if no git)
		root := gitRoot(cwd)
		stop := cwd
		if root != "" {
			stop = root
		}
		d := cwd
		for {
			dirs = append(dirs, scanDir{filepath.Join(d, ".agents", "skills"), "ancestor-agents"})
			if root == "" || sameDir(d, stop) {
				break
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	return dirs
}

func load(path string) (Skill, error) {
	//nolint:gosec // path was discovered under the configured skill roots.
	b, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	s := Skill{FilePath: path, Name: filepath.Base(filepath.Dir(path))}
	text := string(b)
	if strings.HasPrefix(text, "---") {
		if i := strings.Index(text[3:], "---"); i >= 0 {
			fm := text[3 : 3+i]
			text = text[3+i+3:]
			for line := range strings.SplitSeq(fm, "\n") {
				line = strings.TrimSpace(line)
				if k, v, ok := strings.Cut(line, ":"); ok {
					v = strings.TrimSpace(strings.Trim(v, `"'`))
					switch strings.TrimSpace(k) {
					case "name":
						s.Name = v
					case "description":
						s.Description = v
					}
				}
			}
		}
	}
	if s.Description == "" {
		for line := range strings.SplitSeq(text, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				s.Description = line
				break
			}
		}
	}
	return s, nil
}

func gitRoot(cwd string) string {
	d := cwd
	for {
		if st, err := os.Stat(filepath.Join(d, ".git")); err == nil && st != nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			return ""
		}
		d = p
	}
}

func sameDir(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return aa == bb
}
