package skills

import (
	"os"
	"path/filepath"
	"strings"

	"ki/internal/session"
)

// Skill is one discovered skill.
type Skill struct {
	Name        string
	Description string
	FilePath    string
}

// Discover walks the standard skill directories and applies the session toggle.
func Discover(home, cwd string, toggle session.Toggle) []Skill {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "skills"))
	}
	if user, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(user, ".agents", "skills"))
	}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".ki", "skills"))
		// ancestors' .agents/skills up to git root (or cwd only if no git)
		root := gitRoot(cwd)
		stop := cwd
		if root != "" {
			stop = root
		}
		d := cwd
		for {
			dirs = append(dirs, filepath.Join(d, ".agents", "skills"))
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
	seen := map[string]bool{}
	var out []Skill
	for _, dir := range dirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			base := strings.ToLower(info.Name())
			if base != "skill.md" && !(filepath.Dir(path) == dir && strings.HasSuffix(base, ".md") && strings.Contains(dir, string(filepath.Separator)+"skills")) {
				// root .md in ~/.ki/skills and .ki/skills count; SKILL.md anywhere
				if base != "skill.md" {
					if !strings.HasSuffix(dir, "skills") || filepath.Dir(path) != dir || !strings.HasSuffix(base, ".md") {
						return nil
					}
					if strings.Contains(dir, string(filepath.Join(".agents", "skills"))) {
						return nil
					}
				}
			}
			s, err := load(path)
			if err != nil || s.Name == "" {
				return nil
			}
			if seen[s.Name] || !toggle.Allowed(s.Name) {
				return nil
			}
			seen[s.Name] = true
			out = append(out, s)
			return nil
		})
	}
	return out
}

func load(path string) (Skill, error) {
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
			for _, line := range strings.Split(fm, "\n") {
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
		for _, line := range strings.Split(text, "\n") {
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
