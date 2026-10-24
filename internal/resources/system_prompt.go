package resources

import (
	"os"
	"path/filepath"
)

const (
	AppendSourceGlobal  = "global"
	AppendSourceProject = "project"
)

type AppendSystemPrompt struct {
	Source string
	Path   string
	Text   string
}

func AppendSystemPromptPath(home, cwd, source string) (string, bool) {
	switch source {
	case AppendSourceGlobal:
		if home == "" {
			return "", false
		}
		return filepath.Join(home, "prompt", "APPEND_SYSTEM.md"), true
	case AppendSourceProject:
		if cwd == "" {
			return "", false
		}
		return filepath.Join(cwd, ".ki", "prompt", "APPEND_SYSTEM.md"), true
	}
	return "", false
}

func loadAppendSystemPrompts(home, cwd string) []AppendSystemPrompt {
	out := make([]AppendSystemPrompt, 0, 2)
	for _, source := range []string{AppendSourceGlobal, AppendSourceProject} {
		path, ok := AppendSystemPromptPath(home, cwd, source)
		if !ok {
			continue
		}
		text, ok := readPromptFile(path)
		if !ok {
			continue
		}
		out = append(out, AppendSystemPrompt{Source: source, Path: path, Text: text})
	}
	return out
}

func readPromptFile(path string) (string, bool) {
	stat, err := os.Stat(path)
	if err != nil || stat.IsDir() {
		return "", false
	}
	//nolint:gosec // path was discovered under the configured prompt roots.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(content), true
}
