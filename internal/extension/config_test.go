package extension

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"ki/internal/state"
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

func TestExtensionConfigStorageVersionIsPrivateAndProtected(t *testing.T) {
	d := Descriptor{root: t.TempDir(), Config: ConfigSpec{
		Schema: map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"enabled": map[string]any{"type": "boolean"}}},
		Defaults: map[string]any{"enabled": false},
	}}
	got, err := UpdateConfig(d, map[string]any{"enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got["version"]; exists {
		t.Fatal("storage header exposed to HTTP config")
	}
	raw, _, err := state.ReadFile(ConfigPath(d), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["version"] != float64(1) || saved["enabled"] != true {
		t.Fatal(saved)
	}
	if _, err := LoadConfig(d); err != nil {
		t.Fatalf("header entered schema validation: %v", err)
	}
	if _, err := UpdateConfig(d, map[string]any{"version": 2}); err == nil {
		t.Fatal("client changed storage version")
	}
	newer := []byte(`{"version":99,"enabled":false,"future":"keep"}`)
	if err := os.WriteFile(ConfigPath(d), newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(d); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatal(err)
	}
	if _, err := UpdateConfig(d, map[string]any{"enabled": true}); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatal(err)
	}
	gotRaw, err := os.ReadFile(ConfigPath(d))
	if err != nil || string(gotRaw) != string(newer) {
		t.Fatalf("newer config overwritten: %s %v", gotRaw, err)
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
	if !ok || len(accounts) != 1 {
		t.Fatalf("sanitized nested config = %#v", got)
	}
	account, ok := accounts[0].(map[string]any)
	if !ok || account["token"] != SecretValue {
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
