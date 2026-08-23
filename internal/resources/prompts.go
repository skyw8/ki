package resources

import (
	"os"
	"path/filepath"
	"strings"
)

// PromptTemplate is one prompts/*.md slash-command template.
type PromptTemplate struct {
	Name         string
	Description  string
	ArgumentHint string
	Body         string
	Source       string
}

func scanPromptTemplates(home, cwd string) []PromptTemplate {
	byName := map[string]PromptTemplate{}
	loadDir := func(dir, source string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".md") {
				continue
			}
			path := filepath.Join(dir, name)
			template, err := loadPromptTemplate(path, source)
			if err != nil || template.Name == "" {
				continue
			}
			if template.Name == "compact" || template.Name == "reload" {
				continue
			}
			byName[template.Name] = template
		}
	}
	if home != "" {
		loadDir(filepath.Join(home, "prompts"), "home")
	}
	if cwd != "" {
		loadDir(filepath.Join(cwd, ".ki", "prompts"), "project")
	}
	out := make([]PromptTemplate, 0, len(byName))
	for _, template := range byName {
		out = append(out, template)
	}
	return out
}

func loadPromptTemplate(path, source string) (PromptTemplate, error) {
	//nolint:gosec // path was discovered under the configured prompt roots.
	content, err := os.ReadFile(path)
	if err != nil {
		return PromptTemplate{}, err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	template := PromptTemplate{Name: strings.ToLower(base), Source: source, Body: string(content)}
	text := string(content)
	if strings.HasPrefix(text, "---") {
		if i := strings.Index(text[3:], "---"); i >= 0 {
			frontmatter := text[3 : 3+i]
			template.Body = strings.TrimLeft(text[3+i+3:], "\n")
			for line := range strings.SplitSeq(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				key, value, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				value = strings.TrimSpace(strings.Trim(value, `"'`))
				switch strings.TrimSpace(key) {
				case "description":
					template.Description = value
				case "argument-hint":
					template.ArgumentHint = value
				}
			}
		}
	}
	if template.Description == "" {
		for line := range strings.SplitSeq(template.Body, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				template.Description = line
				break
			}
		}
	}
	return template, nil
}
