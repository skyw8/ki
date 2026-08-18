package tools

import (
	"encoding/json"
	"fmt"

	"ki/internal/loop"
)

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// validateArgs runs the tool schema against model arguments (loop ToolValidator).
func validateArgs(schema map[string]any, name string, args map[string]any) error {
	if msg := loop.SchemaErrors(schema, name, args); msg != "" {
		return fmt.Errorf("%w: %s", errToolExecution, msg)
	}
	return nil
}
