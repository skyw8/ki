package support

import (
	"encoding/json"
	"errors"
	"fmt"

	toolapi "ki/internal/tool"
)

func AsInt(v any) (int, bool) {
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

// ValidateArgs runs the tool schema against model arguments (tool.Validator).
func ValidateArgs(schema map[string]any, name string, args map[string]any) error {
	if msg := toolapi.SchemaErrors(schema, name, args); msg != "" {
		return fmt.Errorf("%w: %s", ErrExecution, msg)
	}
	return nil
}

func IntArg(args map[string]any, key string, def int) int {
	if n, ok := AsInt(args[key]); ok {
		return n
	}
	return def
}

func BoolDefault(args map[string]any, key string, def bool) bool {
	if n, ok := args[key].(bool); ok {
		return n
	}
	return def
}

func StringArg(args map[string]any, name, fallback string) string {
	if value, ok := args[name].(string); ok && value != "" {
		return value
	}
	return fallback
}

func ObjectSchema(required []any, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

var ErrExecution = errors.New("tool execution error")
