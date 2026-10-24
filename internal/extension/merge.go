package extension

import (
	"path/filepath"

	"ki/internal/skills"
)

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

type CommandDir struct {
	Path      string
	Extension string
}

func CommandDirs(enabled []Descriptor) []CommandDir {
	var out []CommandDir
	for _, d := range enabled {
		for _, dir := range d.commandDirs() {
			out = append(out, CommandDir{Path: dir, Extension: d.Name})
		}
	}
	return out
}

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

func existingDir(dir string) string {
	if !isDir(dir) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != "" {
		return resolved
	}
	return dir
}
