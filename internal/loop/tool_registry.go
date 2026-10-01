package loop

import (
	"fmt"
	"ki/internal/toolname"
)

type ToolAliases interface{ Aliases() []string }

// ToolRegistry resolves every accepted spelling to one tool instance.
type ToolRegistry struct{ tools map[string]Tool }

func NewToolRegistry(tools []Tool) (*ToolRegistry, error) {
	r := &ToolRegistry{tools: make(map[string]Tool)}
	for _, t := range tools {
		names, err := toolname.Aliases(t.Name())
		if err != nil {
			return nil, err
		}
		if aliases, ok := t.(ToolAliases); ok {
			names = append(names, aliases.Aliases()...)
		}
		seen := make(map[string]bool)
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			if previous, exists := r.tools[n]; exists {
				return nil, fmt.Errorf("tool name collision %q: %s and %s", n, previous.Name(), t.Name())
			}
			r.tools[n] = t
		}
	}
	return r, nil
}
func (r *ToolRegistry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.tools[name]
	return t, ok
}
