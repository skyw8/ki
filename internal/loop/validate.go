package loop

import (
	"fmt"
	"strings"
)

// ValidateSchema is a minimal JSON-Schema subset validator used by
// ToolValidator implementations (P0, pi validateToolArguments): type
// checking per property, required fields, and a top-level object shape.
// It intentionally covers the subset providers and extensions actually
// emit — full JSON Schema (draft-07+) would need a dependency.
//
// schema follows the OpenAI tool schema shape:
//
//	{"type":"object","properties":{"p":{"type":"string",...}},"required":["p"]}
//
// Returns a list of human-readable violations (empty = valid).
func ValidateSchema(schema map[string]any, args map[string]any) []string {
	var errs []string
	if schema == nil {
		return nil
	}
	props, _ := schema["properties"].(map[string]any)
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			name, _ := r.(string)
			if _, ok := args[name]; !ok {
				errs = append(errs, fmt.Sprintf("  - %s: required field", name))
			}
		}
	}
	for name, raw := range props {
		ps, _ := raw.(map[string]any)
		typ, _ := ps["type"].(string)
		val, present := args[name]
		if !present || val == nil {
			continue
		}
		if typ != "" && !typeOK(typ, val) {
			errs = append(errs, fmt.Sprintf("  - %s: expected %s, got %T", name, typ, val))
		}
	}
	return errs
}

func typeOK(typ string, v any) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "integer":
		switch v.(type) {
		case int, int64, int32, float64:
			// float64 with a fractional part is not an integer.
			if f, ok := v.(float64); ok {
				return f == float64(int64(f))
			}
			return true
		}
		return false
	case "number":
		switch v.(type) {
		case int, int64, int32, float64:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return true
}

// SchemaErrors formats ValidateSchema output like pi validateToolArguments:
// a tool named header plus one line per violation, or "" when valid.
func SchemaErrors(schema map[string]any, name string, args map[string]any) string {
	errs := ValidateSchema(schema, args)
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Validation failed for tool %q:\n%s", name, strings.Join(errs, "\n"))
	return b.String()
}
