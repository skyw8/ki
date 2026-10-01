package builtin

import (
	"strings"
	"testing"

	"ki/internal/agent"
	"ki/internal/process"
	"ki/internal/session"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
)

func pick(ts []toolapi.Tool, name string) toolapi.Tool {
	for _, t := range ts {
		if toolapi.Equal(name, t.Name()) {
			return t
		}
	}
	return nil
}

func names(ts []toolapi.Tool) []string {
	out := make([]string, 0, len(ts))
	for _, tool := range ts {
		out = append(out, tool.Name())
	}
	return out
}

func TestBuildSelectsReadCapabilities(t *testing.T) {
	shells := process.DiscoverShellRuntime()
	set := Set{CWD: t.TempDir(), Shells: shells}
	classic := set.Build(Profile{})
	wantClassic := []string{"read", "write", "edit", "grep", "glob", "exec_command", "write_stdin"}
	if got := strings.Join(names(classic), ","); got != strings.Join(wantClassic, ",") {
		t.Fatalf("classic tools = %s", got)
	}
	textRead := pick(classic, "Read")
	properties, ok := textRead.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("text Read properties = %#v", textRead.Parameters()["properties"])
	}
	if strings.Contains(textRead.Prompt(), "PDF") || properties["pages"] != nil {
		t.Fatalf("text Read leaked rich capabilities: %s %+v", textRead.Prompt(), textRead.Parameters())
	}

	patch := set.Build(Profile{RichRead: true, ApplyPatch: true})
	wantPatch := []string{"read", "apply_patch", "grep", "glob", "exec_command", "write_stdin"}
	if got := strings.Join(names(patch), ","); got != strings.Join(wantPatch, ",") {
		t.Fatalf("patch tools = %s", got)
	}
	if !strings.Contains(pick(patch, "Read").Prompt(), "PDF") {
		t.Fatal("rich Read omitted PDF capability")
	}
	provider, ok := pick(patch, "apply_patch").(toolapi.SpecProvider)
	if !ok {
		t.Fatal("apply_patch does not provide a custom tool spec")
	}
	spec := provider.ToolSpec()
	if spec.Type != "custom" || spec.Format == nil || spec.Format.Syntax != "lark" {
		t.Fatalf("apply_patch spec = %+v", spec)
	}
	if !strings.Contains(spec.Description, "Include each file path once") {
		t.Fatalf("apply_patch description omits single-path rule: %q", spec.Description)
	}
	catalog := set.Catalog(Profile{ApplyPatch: true})
	for _, name := range []string{"apply_patch", "Write", "Edit"} {
		if pick(catalog, name) == nil {
			t.Fatalf("catalog omitted %s: %v", name, names(catalog))
		}
	}
}

func TestFilterBuiltinsHonorsToggle(t *testing.T) {
	store := agent.NewController()
	defer store.Close()
	set := Set{CWD: t.TempDir(), Agent: fakeAgentRuntime{store: store}}
	all := set.Build(Profile{})
	filtered := FilterBuiltins(all, session.Toggle{Disabled: []string{"Read", "SpawnAgent"}})
	if pick(filtered, "Read") != nil {
		t.Fatal("Read was not disabled")
	}
	if pick(filtered, "SpawnAgent") != nil {
		t.Fatal("Agent was not disabled")
	}
	if pick(filtered, "Grep") == nil {
		t.Fatal("unlisted built-in tool was disabled")
	}
}

func TestExecCommandContract(t *testing.T) {
	manager := process.NewManager()
	defer manager.Close()
	tool := pick(Set{CWD: t.TempDir(), Processes: manager}.Build(Profile{}), "ExecCommand")
	props := tool.Parameters()["properties"].(map[string]any)
	for _, name := range []string{"cmd", "workdir", "shell", "login", "tty", "yield_time_ms", "max_output_tokens"} {
		if props[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	for _, name := range []string{"command", "timeout", "run_in_background"} {
		if props[name] != nil {
			t.Fatalf("legacy argument %s", name)
		}
	}
	if tool.(toolapi.Validator).Validate(map[string]any{"cmd": "printf ok"}) != nil || tool.(toolapi.Validator).Validate(map[string]any{"command": "printf ok"}) == nil {
		t.Fatal("exec schema mismatch")
	}
}

func TestCatalogMatchesReservedIdentifiers(t *testing.T) {
	controller := agent.NewController()
	defer controller.Close()
	set := Set{CWD: t.TempDir(), Agent: fakeAgentRuntime{store: controller}}
	registered := map[string]bool{}
	for _, implementation := range set.Catalog(Profile{}) {
		name := implementation.Name()
		if registered[name] || !catalog.IsReserved(name) || !catalog.IsReserved(toolapi.Pascal(name)) {
			t.Fatalf("duplicate or unreserved builtin %q", name)
		}
		registered[name] = true
	}
	for _, name := range catalog.Names() {
		if !registered[name] {
			t.Fatalf("reserved builtin %q has no catalog implementation", name)
		}
	}
}
