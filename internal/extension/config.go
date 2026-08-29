package extension

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// SecretValue is the redacted value returned for a configured secret. Clients
// may send it back unchanged, but it is never written as the real secret.
const SecretValue = "<configured>"

// ConfigPath returns the private configuration file for one extension.
func ConfigPath(d Descriptor) string { return filepath.Join(d.root, "config.json") }

// LoadConfig loads defaults and the extension's persisted values. The returned
// map is a detached JSON value and is safe for callers to modify.
func LoadConfig(d Descriptor) (map[string]any, error) {
	values := cloneConfigMap(d.Config.Defaults)
	b, err := os.ReadFile(ConfigPath(d))
	if err != nil {
		if os.IsNotExist(err) {
			return values, validateConfig(d.Config.Schema, values)
		}
		return nil, fmt.Errorf("read extension config: %w", err)
	}
	var saved map[string]any
	if err := json.Unmarshal(b, &saved); err != nil || saved == nil {
		if err == nil {
			err = errConfigMustBeObject
		}
		return nil, fmt.Errorf("decode extension config: %w", err)
	}
	values = mergeConfig(values, saved, d.Config.Schema)
	if err := validateConfig(d.Config.Schema, values); err != nil {
		return nil, err
	}
	return values, nil
}

// SanitizedConfig returns the current configuration with secret fields
// replaced by SecretValue. The schema is returned separately by the HTTP API.
func SanitizedConfig(d Descriptor) (map[string]any, error) {
	values, err := LoadConfig(d)
	if err != nil {
		return nil, err
	}
	return sanitizedConfigMap(d.Config.Schema, values), nil
}

// UpdateConfig merges a partial JSON object, validates the result, and writes
// it with private permissions. Omitting a secret preserves its current value.
func UpdateConfig(d Descriptor, patch map[string]any) (map[string]any, error) {
	if patch == nil {
		patch = map[string]any{}
	}
	current, err := LoadConfig(d)
	if err != nil {
		return nil, err
	}
	values := mergeConfig(current, patch, d.Config.Schema)
	if err := validateConfig(d.Config.Schema, values); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode extension config: %w", err)
	}
	if err := os.MkdirAll(d.root, 0o700); err != nil {
		return nil, fmt.Errorf("create extension config directory: %w", err)
	}
	if err := os.WriteFile(ConfigPath(d), append(b, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write extension config: %w", err)
	}
	return sanitizedConfigMap(d.Config.Schema, values), nil
}

func sanitizedConfigMap(schema map[string]any, values map[string]any) map[string]any {
	out, _ := sanitizeConfig(schema, values).(map[string]any)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func cloneConfigMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func mergeConfig(base, patch map[string]any, schema map[string]any) map[string]any {
	out := cloneConfigMap(base)
	properties := schemaProperties(schema)
	for key, value := range patch {
		property, _ := properties[key].(map[string]any)
		if value == nil {
			delete(out, key)
			continue
		}
		out[key] = mergeConfigValue(out[key], value, property)
	}
	return out
}

func mergeConfigValue(base, patch any, schema map[string]any) any {
	if schema != nil && schema["secret"] == true && patch == SecretValue {
		return cloneConfigValue(base)
	}
	if next, ok := patch.(map[string]any); ok {
		previous, _ := base.(map[string]any)
		return mergeConfig(previous, next, schema)
	}
	if next, ok := patch.([]any); ok {
		previous, _ := base.([]any)
		items, _ := schema["items"].(map[string]any)
		out := make([]any, len(next))
		for i, value := range next {
			var old any
			if i < len(previous) {
				old = previous[i]
			}
			out[i] = mergeConfigValue(old, value, items)
		}
		return out
	}
	return cloneConfigValue(patch)
}

func cloneConfigValue(value any) any {
	b, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return value
	}
	return out
}

func sanitizeConfig(schema map[string]any, value any) any {
	if value == nil {
		return nil
	}
	if schema["secret"] == true {
		if strings.TrimSpace(fmt.Sprint(value)) == "" {
			return ""
		}
		return SecretValue
	}
	if values, ok := value.(map[string]any); ok {
		properties := schemaProperties(schema)
		out := make(map[string]any, len(values))
		for key, child := range values {
			childSchema, _ := properties[key].(map[string]any)
			out[key] = sanitizeConfig(childSchema, child)
		}
		return out
	}
	if values, ok := value.([]any); ok {
		items, _ := schema["items"].(map[string]any)
		out := make([]any, len(values))
		for i, child := range values {
			out[i] = sanitizeConfig(items, child)
		}
		return out
	}
	return value
}

func schemaProperties(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	return properties
}

func validateConfig(schema map[string]any, value any) error {
	if len(schema) == 0 {
		return nil
	}
	return validateConfigValue(schema, value, "config")
}

func validateConfigValue(schema map[string]any, value any, path string) error {
	if value == nil {
		if required, _ := schema["required"].(bool); required {
			return fmt.Errorf("%s: %w", path, errConfigRequired)
		}
		return nil
	}
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		values, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: %w", path, errConfigObject)
		}
		properties := schemaProperties(schema)
		required := map[string]bool{}
		for _, name := range schemaStrings(schema["required"]) {
			required[name] = true
		}
		for key, childSchemaRaw := range properties {
			childSchema, _ := childSchemaRaw.(map[string]any)
			child, exists := values[key]
			if !exists && required[key] {
				return fmt.Errorf("%s.%s: %w", path, key, errConfigRequired)
			}
			if err := validateConfigValue(childSchema, child, path+"."+key); err != nil {
				return err
			}
		}
		if additional, exists := schema["additionalProperties"]; exists && additional == false {
			for key := range values {
				if _, ok := properties[key]; !ok {
					return fmt.Errorf("%s.%s: %w", path, key, errConfigNotAllowed)
				}
			}
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: %w", path, errConfigArray)
		}
		if minItems, ok := number(schema["minItems"]); ok && float64(len(values)) < minItems {
			return fmt.Errorf("%s: %w", path, errConfigTooFewItems)
		}
		if maxItems, ok := number(schema["maxItems"]); ok && float64(len(values)) > maxItems {
			return fmt.Errorf("%s: %w", path, errConfigTooManyItems)
		}
		items, _ := schema["items"].(map[string]any)
		for i, child := range values {
			if err := validateConfigValue(items, child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: %w", path, errConfigString)
		}
		if minLen, ok := number(schema["minLength"]); ok && float64(len([]rune(text))) < minLen {
			return fmt.Errorf("%s: %w", path, errConfigTooShort)
		}
		if maxLen, ok := number(schema["maxLength"]); ok && float64(len([]rune(text))) > maxLen {
			return fmt.Errorf("%s: %w", path, errConfigTooLong)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: %w", path, errConfigBoolean)
		}
	case "number":
		if _, ok := number(value); !ok {
			return fmt.Errorf("%s: %w", path, errConfigNumber)
		}
	case "integer":
		n, ok := number(value)
		if !ok || math.Trunc(n) != n {
			return fmt.Errorf("%s: %w", path, errConfigInteger)
		}
	}
	if enum := schemaValues(schema["enum"]); len(enum) > 0 {
		matched := false
		for _, item := range enum {
			if fmt.Sprint(item) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: %w", path, errConfigInvalidValue)
		}
	}
	return nil
}

func schemaStrings(value any) []string {
	switch values := value.(type) {
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return append([]string(nil), values...)
	default:
		return nil
	}
}

func schemaValues(value any) []any {
	switch values := value.(type) {
	case []any:
		return append([]any(nil), values...)
	case []string:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out
	default:
		return nil
	}
}

func number(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		v, err := n.Float64()
		return v, err == nil
	default:
		return 0, false
	}
}
