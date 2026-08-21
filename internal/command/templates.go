package command

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Template is one prompts/*.md file.
type Template struct {
	Name         string
	Description  string
	ArgumentHint string
	Body         string
	Source       string
}

type tmplEnt struct {
	items []Template
}

var (
	tmplMu    sync.Mutex
	tmplCache = map[string]tmplEnt{}
)

var placeholderRe = regexp.MustCompile(`\$\{(\d+|ARGUMENTS|@):-([^}]*)\}|\$\{@:(\d+)(?::(\d+))?\}|\$(ARGUMENTS|@|\d+)`)

// InvalidateAll drops cached prompt templates.
func InvalidateAll() {
	tmplMu.Lock()
	defer tmplMu.Unlock()
	tmplCache = map[string]tmplEnt{}
}

func listTemplates(home, cwd, sessionID string) []Template {
	key := home + "\x00" + cwd + "\x00" + sessionID
	tmplMu.Lock()
	defer tmplMu.Unlock()
	if e, ok := tmplCache[key]; ok {
		return e.items
	}
	items := scanTemplates(home, cwd)
	tmplCache[key] = tmplEnt{items: items}
	return items
}

func scanTemplates(home, cwd string) []Template {
	byName := map[string]Template{}
	loadDir := func(dir, source string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".md") {
				continue
			}
			p := filepath.Join(dir, name)
			t, err := loadTemplate(p, source)
			if err != nil || t.Name == "" {
				continue
			}
			if t.Name == "compact" || t.Name == "reload" {
				continue
			}
			byName[t.Name] = t
		}
	}
	if home != "" {
		loadDir(filepath.Join(home, "prompts"), "home")
	}
	if cwd != "" {
		loadDir(filepath.Join(cwd, ".ki", "prompts"), "project")
	}
	out := make([]Template, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	return out
}

func loadTemplate(path, source string) (Template, error) {
	b, err := os.ReadFile(path) //nolint:gosec // discovered under prompt roots
	if err != nil {
		return Template{}, err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	t := Template{Name: strings.ToLower(base), Source: source, Body: string(b)}
	text := string(b)
	if strings.HasPrefix(text, "---") {
		if i := strings.Index(text[3:], "---"); i >= 0 {
			fm := text[3 : 3+i]
			t.Body = strings.TrimLeft(text[3+i+3:], "\n")
			for line := range strings.SplitSeq(fm, "\n") {
				line = strings.TrimSpace(line)
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				v = strings.TrimSpace(strings.Trim(v, `"'`))
				switch strings.TrimSpace(k) {
				case "description":
					t.Description = v
				case "argument-hint":
					t.ArgumentHint = v
				}
			}
		}
	}
	if t.Description == "" {
		for line := range strings.SplitSeq(t.Body, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Description = line
				break
			}
		}
	}
	return t, nil
}

func templateByName(home, cwd, sessionID, name string) (Template, bool) {
	for _, t := range listTemplates(home, cwd, sessionID) {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

// ExpandTemplate substitutes $1 / $@ style args. Unknown name returns false.
func ExpandTemplate(home, cwd, sessionID, name, args string) (string, bool) {
	t, ok := templateByName(home, cwd, sessionID, name)
	if !ok {
		return "", false
	}
	return substituteArgs(t.Body, parseCommandArgs(args)), true
}

func parseCommandArgs(s string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func substituteArgs(content string, args []string) string {
	all := strings.Join(args, " ")
	return placeholderRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := placeholderRe.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		if sub[1] != "" {
			val := all
			if sub[1] != "@" && sub[1] != "ARGUMENTS" {
				i, _ := strconv.Atoi(sub[1])
				if i >= 1 && i <= len(args) {
					val = args[i-1]
				} else {
					val = ""
				}
			}
			if val != "" {
				return val
			}
			return sub[2]
		}
		if sub[3] != "" {
			start, _ := strconv.Atoi(sub[3])
			if start < 1 {
				start = 1
			}
			from := start - 1
			if from > len(args) {
				from = len(args)
			}
			if sub[4] != "" {
				n, _ := strconv.Atoi(sub[4])
				end := from + n
				if end > len(args) {
					end = len(args)
				}
				return strings.Join(args[from:end], " ")
			}
			return strings.Join(args[from:], " ")
		}
		switch sub[5] {
		case "ARGUMENTS", "@":
			return all
		default:
			i, _ := strconv.Atoi(sub[5])
			if i >= 1 && i <= len(args) {
				return args[i-1]
			}
			return ""
		}
	})
}
