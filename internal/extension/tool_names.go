package extension

import (
	"fmt"
	"ki/internal/toolname"
)

// Validation precedes registration so collisions never replace a live capability.
func validateToolNames(specs []ToolSpec) error {
	seen := map[string]string{}
	for _, spec := range specs {
		aliases, err := toolname.Aliases(spec.Name)
		if err != nil {
			return err
		}
		if toolname.IsBuiltin(toolname.MustCanonical(spec.Name)) {
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
