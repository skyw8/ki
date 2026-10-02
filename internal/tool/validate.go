package tool

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ValidateSchema is a minimal JSON-Schema subset validator used by
// Validator implementations (P0, pi validateToolArguments): type
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
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for name := range args {
			if _, known := props[name]; !known {
				errs = append(errs, fmt.Sprintf("  - %s: additional property is not allowed", name))
			}
		}
	}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			name, _ := r.(string)
			if value, ok := args[name]; !ok || value == nil {
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
		switch n := v.(type) {
		case int, int64, int32:
			return true
		case float64:
			// Why: float64 rounds MaxInt64 up to 2^63, so the upper bound
			// must be exclusive before any builtin converts it to an integer.
			return n >= -0x1p63 && n < 0x1p63 && math.Trunc(n) == n
		case json.Number:
			_, err := n.Int64()
			return err == nil
		}
		return false
	case "number":
		switch n := v.(type) {
		case int, int64, int32:
			return true
		case float64:
			return !math.IsNaN(n) && !math.IsInf(n, 0)
		case json.Number:
			f, err := n.Float64()
			return err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
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
