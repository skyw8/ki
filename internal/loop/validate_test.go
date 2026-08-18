package loop

import (
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
		{"null ignored", map[string]any{"file_path": "/a", "content": nil}, nil},
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
