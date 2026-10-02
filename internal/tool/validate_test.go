package tool

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestValidateSchema(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"file_path", "content"},
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string"},
			"offset":    map[string]any{"type": "integer"},
			"limit":     map[string]any{"type": "integer"},
			"timeout":   map[string]any{"type": "number"},
			"flag":      map[string]any{"type": "boolean"},
			"items":     map[string]any{"type": "array"},
			"meta":      map[string]any{"type": "object"},
		},
	}
	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"valid", map[string]any{"file_path": "/a", "content": "x"}, nil},
		{"missing required", map[string]any{"file_path": "/a"}, []string{"  - content: required field"}},
		{"missing both", map[string]any{}, []string{"  - file_path: required field", "  - content: required field"}},
		{"wrong type string", map[string]any{"file_path": "/a", "content": "x", "offset": "oops"}, []string{"  - offset: expected integer, got string"}},
		{"integer as float ok", map[string]any{"file_path": "/a", "content": "x", "offset": float64(3)}, nil},
		{"float not integer", map[string]any{"file_path": "/a", "content": "x", "offset": float64(3.5)}, []string{"  - offset: expected integer, got float64"}},
		{"number accepts int", map[string]any{"file_path": "/a", "content": "x", "timeout": 5000}, nil},
		{"boolean ok", map[string]any{"file_path": "/a", "content": "x", "flag": true}, nil},
		{"array ok", map[string]any{"file_path": "/a", "content": "x", "items": []any{1, 2}}, nil},
		{"object ok", map[string]any{"file_path": "/a", "content": "x", "meta": map[string]any{"k": 1}}, nil},
		{"required null", map[string]any{"file_path": "/a", "content": nil}, []string{"  - content: required field"}},
		{"optional null ignored", map[string]any{"file_path": "/a", "content": "x", "offset": nil}, nil},
	}
	for _, c := range cases {
		got := ValidateSchema(schema, c.args)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: err %d = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestSchemaNumericRepresentations(t *testing.T) {
	for _, typ := range []string{"integer", "number"} {
		schema := map[string]any{"properties": map[string]any{"value": map[string]any{"type": typ}}}
		for _, value := range []any{int32(3), int64(3), float64(3), json.Number("3")} {
			if errs := ValidateSchema(schema, map[string]any{"value": value}); len(errs) != 0 {
				t.Fatalf("%s rejected %T: %v", typ, value, errs)
			}
		}
		for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1)} {
			if errs := ValidateSchema(schema, map[string]any{"value": value}); len(errs) == 0 {
				t.Fatalf("%s accepted non-finite %v", typ, value)
			}
		}
	}
	schema := map[string]any{"properties": map[string]any{"value": map[string]any{"type": "integer"}}}
	for _, value := range []any{float64(0x1p63), math.Nextafter(-0x1p63, math.Inf(-1)), json.Number("9223372036854775808")} {
		if errs := ValidateSchema(schema, map[string]any{"value": value}); len(errs) == 0 {
			t.Fatalf("integer accepted unrepresentable %v", value)
		}
	}
}

func TestSchemaErrorsFormatting(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"cmd"}}
	msg := SchemaErrors(schema, "Bash", map[string]any{})
	if !strings.HasPrefix(msg, "Validation failed for tool \"Bash\":") || !strings.Contains(msg, "cmd: required field") {
		t.Fatalf("format: %q", msg)
	}
	if SchemaErrors(schema, "Bash", map[string]any{"cmd": "ls"}) != "" {
		t.Fatal("valid args should produce no errors")
	}
	if SchemaErrors(nil, "Bash", map[string]any{}) != "" {
		t.Fatal("nil schema should validate everything")
	}
}

func TestValidateSchemaRejectsAdditionalProperties(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"prompt": map[string]any{"type": "string"}},
	}
	errs := ValidateSchema(schema, map[string]any{"prompt": "work", "model": "other/model"})
	if len(errs) != 1 || !strings.Contains(errs[0], "model: additional property is not allowed") {
		t.Fatalf("additional property errors = %v", errs)
	}
}
