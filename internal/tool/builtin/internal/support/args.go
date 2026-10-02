package support

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	toolapi "ki/internal/tool"
)

func AsInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), int64(int(n)) == n
	case float64:
		// Why: converting an out-of-range float to int is implementation-dependent.
		// A wrapped handle or wait budget must never reach a runtime operation.
		limit := math.Ldexp(1, strconv.IntSize-1)
		if math.IsNaN(n) || n < -limit || n >= limit || math.Trunc(n) != n {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil && int64(int(i)) == i
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
