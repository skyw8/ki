package provider

import (
	"errors"
	"testing"
)

func TestBuiltinNativeToolCapabilities(t *testing.T) {
	for _, p := range BuiltinProviders() {
		for _, m := range p.Models {
			want := p.ID == "openai"
			if m.SupportsFreeformExec() != want || m.SupportsFreeformApplyPatch() != want {
				t.Errorf("%s/%s native tool capabilities: exec=%q patch=%q API=%q", p.ID, m.ID, m.ExecToolType, m.ApplyPatchToolType, m.API)
			}
		}
	}
}

func TestNativeToolCapabilitiesRequireCompatibleTransport(t *testing.T) {
	for _, api := range []string{"responses", "completions", "anthropic", "openai-codex-responses", "extension-private"} {
		t.Run(api, func(t *testing.T) {
			m := Model{API: api, ApplyPatchToolType: "freeform"}
			want := api != "completions" && api != "anthropic"
			if m.SupportsFreeformExec() || m.SupportsFreeformApplyPatch() != want {
				t.Fatal("patch capability incorrectly grants exec or ignores transport")
			}
			m.ExecToolType = "freeform"
			m.ApplyPatchToolType = ""
			if m.SupportsFreeformExec() != want || m.SupportsFreeformApplyPatch() {
				t.Fatal("exec capability incorrectly grants patch or ignores transport")
			}
		})
	}
}

func TestRegistryExecCapabilityOverridePersistsAndClears(t *testing.T) {
	home := t.TempDir()
	r, err := NewRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(value string) {
		t.Helper()
		err := r.Update(func(cfg *ModelsFile) error {
			cfg.Providers["deepseek"] = Config{
				API:            "responses",
				ModelOverrides: map[string]ModelOverride{"deepseek-flash": {ExecToolType: &value}},
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		r, err = NewRegistry(home)
		if err != nil {
			t.Fatal(err)
		}
		_, m, ok := r.FindModel("deepseek", "deepseek-flash")
		if !ok || m.ExecToolType != value || m.ApplyPatchToolType != "" || m.SupportsFreeformExec() != (value == "freeform") {
			t.Fatalf("exec override resolved incorrectly: %+v", m)
		}
	}
	apply("freeform")
	apply("")
	bad := "function"
	err = r.Update(func(cfg *ModelsFile) error {
		cfg.Providers["deepseek"] = Config{ModelOverrides: map[string]ModelOverride{"deepseek-flash": {ExecToolType: &bad}}}
		return nil
	})
	if !errors.Is(err, errInvalidExecToolType) {
		t.Fatalf("invalid override error = %v", err)
	}
}

func TestExtensionProviderExecCapability(t *testing.T) {
	spec := ExtensionProviderSpec{
		ID: "custom", Name: "Custom", API: "extension-private", BaseURL: "https://example.com",
		Models: []ModelSeed{{ID: "model", ExecToolType: "freeform"}},
	}
	p, err := BuildExtensionProvider(spec)
	if err != nil || len(p.Models) != 1 || !p.Models[0].SupportsFreeformExec() || p.Models[0].SupportsFreeformApplyPatch() {
		t.Fatalf("extension exec capability = %+v, error = %v", p, err)
	}
	spec.Models[0].ExecToolType = "function"
	if _, err := BuildExtensionProvider(spec); !errors.Is(err, errInvalidExecToolType) {
		t.Fatalf("invalid extension capability error = %v", err)
	}
}
