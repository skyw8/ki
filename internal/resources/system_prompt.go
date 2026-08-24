package resources

import (
	"os"
	"path/filepath"
)

// loadAppendSystemPrompt loads the optional system-prompt supplement. The
// project file wins over the global file, matching the override behavior of
// the other project-scoped prompt resources.
func loadAppendSystemPrompt(home, cwd string) string {
	if cwd != "" {
		if content, ok := readPromptFile(filepath.Join(cwd, ".ki", "prompt", "APPEND_SYSTEM.md")); ok {
			return content
		}
	}
	if home != "" {
		if content, ok := readPromptFile(filepath.Join(home, "prompt", "APPEND_SYSTEM.md")); ok {
			return content
		}
	}
	return ""
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
