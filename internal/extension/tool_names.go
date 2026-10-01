package extension

import (
	"fmt"

	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
)

// Validation precedes registration so collisions never replace a live capability.
func validateToolNames(specs []ToolSpec) error {
	seen := map[string]string{}
	for _, spec := range specs {
		aliases, err := toolapi.Aliases(spec.Name)
		if err != nil {
			return err
		}
		if catalog.IsReserved(toolapi.MustCanonical(spec.Name)) {
			return fmt.Errorf("reserved tool name %q", spec.Name)
		}
		for _, name := range aliases {
			if previous, ok := seen[name]; ok {
				return fmt.Errorf("tool name collision %q: %s and %s", name, previous, spec.Name)
			}
			seen[name] = spec.Name
		}
	}
	return nil
}
