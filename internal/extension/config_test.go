package extension

import (
	"os"
	"strings"
	"testing"
)

func TestExtensionConfigRedactsAndPreservesSecrets(t *testing.T) {
	d := Descriptor{
		Name: "telegram-bot",
		root: t.TempDir(),
		Config: ConfigSpec{
			Schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"token":   map[string]any{"type": "string", "secret": true},
					"enabled": map[string]any{"type": "boolean"},
				},
			},
			Defaults: map[string]any{"token": "", "enabled": false},
		},
	}

	got, err := UpdateConfig(d, map[string]any{"token": "bot-secret", "enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if got["token"] != SecretValue || got["enabled"] != true {
		t.Fatalf("sanitized config = %#v", got)
	}
	raw, err := os.ReadFile(ConfigPath(d))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "bot-secret") {
		t.Fatalf("secret was not persisted: %s", raw)
	}

	got, err = UpdateConfig(d, map[string]any{"token": SecretValue})
	if err != nil {
		t.Fatal(err)
	}
	if got["token"] != SecretValue {
		t.Fatalf("redacted update changed secret: %#v", got)
	}
	if _, err := UpdateConfig(d, map[string]any{"enabled": "yes"}); err == nil {
		t.Fatal("invalid config value was accepted")
	}
}

func TestExtensionConfigPreservesNestedArraySecrets(t *testing.T) {
	d := Descriptor{
		Name: "telegram-bot",
		root: t.TempDir(),
		Config: ConfigSpec{
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"accounts": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"accountId": map[string]any{"type": "string"},
								"token":     map[string]any{"type": "string", "secret": true},
							},
						},
					},
				},
			},
			Defaults: map[string]any{"accounts": []any{}},
		},
	}
	if _, err := UpdateConfig(d, map[string]any{"accounts": []any{map[string]any{"accountId": "bot-1", "token": "secret"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := UpdateConfig(d, map[string]any{"accounts": []any{map[string]any{"accountId": "bot-1", "token": SecretValue}}})
	if err != nil {
		t.Fatal(err)
	}
	accounts, ok := got["accounts"].([]any)
	if !ok || len(accounts) != 1 || accounts[0].(map[string]any)["token"] != SecretValue {
		t.Fatalf("sanitized nested config = %#v", got)
	}
	raw, err := os.ReadFile(ConfigPath(d))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "secret") || strings.Contains(string(raw), SecretValue) {
		t.Fatalf("nested secret was not preserved safely: %s", raw)
	}
}
