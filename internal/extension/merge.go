package extension

import (
	"strings"

	"ki/internal/mcp"
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

// SkillRoots returns extra skill directories: project packages then home
// packages, matching docs/extension.md skill root order.
func SkillRoots(enabled []Descriptor) []skills.Root {
	var project, home []skills.Root
	for _, d := range enabled {
		source := "extension:" + d.Name
		for _, root := range d.skillRoots() {
			r := skills.Root{Path: root, Source: source}
			if d.Scope == "project" {
				project = append(project, r)
			} else {
				home = append(home, r)
			}
		}
	}
	return append(project, home...)
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

// MergeMCP adds enabled extension MCP specs. Existing .mcp.json names win.
// First extension to claim a remaining server name wins.
func MergeMCP(base mcp.File, enabled []Descriptor) mcp.File {
	if base.MCPServers == nil {
		base.MCPServers = map[string]mcp.ServerSpec{}
	}
	if base.Sources == nil {
		base.Sources = map[string]string{}
	}
	for _, d := range enabled {
		if !hasKind(d.Capabilities, CapMCP) {
			continue
		}
		for name, spec := range d.manifest.MCP.MCPServers {
			if _, exists := base.MCPServers[name]; exists {
				continue
			}
			if err := mcp.ValidateServerSpec(spec); err != nil {
				continue
			}
			base.MCPServers[name] = spec
			base.Sources[name] = "extension:" + d.Name
		}
	}
	return base
}

// ExtensionIDFromSource returns the package name if source is extension:<id>.
func ExtensionIDFromSource(source string) string {
	const p = "extension:"
	if strings.HasPrefix(source, p) {
		return strings.TrimPrefix(source, p)
	}
	return ""
}

// PrefixMCPTools rewrites model-facing names for tools bound from extension
// MCP servers. CallTool uses WireName.
func PrefixMCPTools(defs []mcp.ToolDefinition, extensionName string) []mcp.ToolDefinition {
	if extensionName == "" {
		return defs
	}
	out := make([]mcp.ToolDefinition, 0, len(defs))
	seen := map[string]bool{}
	for _, def := range defs {
		wire := def.Name
		if def.WireName != "" {
			wire = def.WireName
		}
		if strings.Contains(wire, "/") {
			continue
		}
		name := extensionName + "/" + wire
		if seen[name] {
			continue
		}
		seen[name] = true
		def.WireName = wire
		def.Name = name
		out = append(out, def)
	}
	return out
}
