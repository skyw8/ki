package resources

import (
	"os"
	"path/filepath"
)

// Sources of system-prompt supplement files. Both files are additive: the
// global file and the project file are concatenated in this order, so a project
// file can add to or refine the global one instead of hiding it.
const (
	AppendSourceGlobal  = "global"
	AppendSourceProject = "project"
)

// AppendSystemPrompt is one supplement file layer that exists on disk. The
// settings view lists these per source and prompt.Build renders their text.
type AppendSystemPrompt struct {
	Source string
	Path   string
	Text   string
}

// AppendSystemPromptPath resolves the file backing one source. The loader and
// the settings writer both go through it, so the read path and the write path
// cannot drift apart. The second result is false for an unknown source and for
// a project source with no cwd to resolve against.
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

// loadAppendSystemPrompts reads every supplement file that exists, global first.
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
