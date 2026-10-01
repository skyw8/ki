package extension

import "testing"

func TestExtensionNativeAcronymAliasesAndAtomicNameValidation(t *testing.T) {
	tool := sidecarTool{spec: ToolSpec{Name: "HTTPFetch"}}
	if tool.Name() != "http_fetch" {
		t.Fatal(tool.Name())
	}
	aliases := tool.Aliases()
	for _, want := range []string{"HTTPFetch", "HttpFetch", "http_fetch"} {
		found := false
		for _, name := range aliases {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing alias %s: %v", want, aliases)
		}
	}
	// The adapter retains the RPC contract's original name alongside its model name.
	if tool.spec.Name != "HTTPFetch" {
		t.Fatal("native RPC name overwritten")
	}
	for _, specs := range [][]ToolSpec{{{Name: "READ"}}, {{Name: "ExecCommand"}}, {{Name: "HTTPFetch"}, {Name: "http_fetch"}}, {{Name: "read__remote"}, {Name: "read_remote"}}, {{Name: "bad-name"}}} {
		if err := validateToolNames(specs); err == nil {
			t.Fatalf("accepted invalid/conflicting batch %+v", specs)
		}
	}
	if err := validateToolNames([]ToolSpec{{Name: "HTTPFetch"}, {Name: "fetch_local"}}); err != nil {
		t.Fatal(err)
	}
}
