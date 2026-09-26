package extension

import (
	"path/filepath"

	"ki/internal/skills"
)

// PromptLayers returns enabled append texts in chain order.
func PromptLayers(enabled []Descriptor) []PromptLayer {
	var out []PromptLayer
	for _, d := range enabled {
		text := d.promptText()
		if text == "" {
			continue
		}
		out = append(out, PromptLayer{ExtensionID: d.Name, Text: text})
	}
	return out
}

// SkillRoots returns extra skill directories from globally discovered packages.
func SkillRoots(enabled []Descriptor) []skills.Root {
	var out []skills.Root
	for _, d := range enabled {
		source := "extension:" + d.Name
		for _, root := range d.skillRoots() {
			out = append(out, skills.Root{Path: root, Source: source})
		}
	}
	return out
}

// CommandDir is one commands/ folder contributed by an enabled package.
type CommandDir struct {
	Path      string
	Extension string
}

// CommandDirs lists slash-template directories from enabled packages.
func CommandDirs(enabled []Descriptor) []CommandDir {
	var out []CommandDir
	for _, d := range enabled {
		for _, dir := range d.commandDirs() {
			out = append(out, CommandDir{Path: dir, Extension: d.Name})
		}
	}
	return out
}

// PathDirs returns the PATH directories contributed by enabled packages in
// chain order, deduplicated, keeping only those that exist right now. Existence
// is resolved per call rather than at load time because runtime.install can
// create the directory (node_modules/.bin) after the manifest was validated.
func PathDirs(enabled []Descriptor) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range enabled {
		if !d.Enabled {
			continue
		}
		for _, declared := range d.PathDirs() {
			dir := existingDir(declared)
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

// existingDir returns the resolved absolute path when dir is an existing
// directory, and "" otherwise.
func existingDir(dir string) string {
	if !isDir(dir) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != "" {
		return resolved
	}
	return dir
}
