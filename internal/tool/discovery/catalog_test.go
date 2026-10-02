package discovery

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

type testTool struct {
	name, description, source, sourceDescription string
	params                                       map[string]any
}

func (t *testTool) Name() string                                         { return t.name }
func (t *testTool) Description() string                                  { return t.description }
func (*testTool) Prompt() string                                         { return "" }
func (*testTool) Snippet() string                                        { return "" }
func (t *testTool) Parameters() map[string]any                           { return t.params }
func (*testTool) Execute(context.Context, map[string]any) toolapi.Result { return toolapi.Result{} }
func (t *testTool) SourceInfo() (string, string)                         { return t.source, t.sourceDescription }

func fixture(name, description string) *testTool {
	return &testTool{name: name, description: description, source: "Documents", sourceDescription: "Search and update shared documents", params: map[string]any{
		"type": "object", "properties": map[string]any{"document_id": map[string]any{"type": "string", "description": "Document identifier"}},
		"required": []any{"document_id"}, "additionalProperties": false,
	}}
}

func resultNames(t *testing.T, result toolapi.Result) []string {
	t.Helper()
	raw, err := json.Marshal(result.Details)
	if err != nil {
		t.Fatal(err)
	}
	var details struct {
		Names []string `json:"discovered_tools"`
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatal(err)
	}
	return details.Names
}

func TestSearchBM25MetadataCompleteSchemasAndSources(t *testing.T) {
	read := fixture("mcp_docs_read", "Read a document")
	write := fixture("mcp_docs_write", "Write a document")
	c := New([]toolapi.Tool{write, read})
	search := c.SearchTool()
	if !strings.Contains(search.Prompt(), "Documents") || strings.Contains(c.SourceSummary(), read.Name()) {
		t.Fatalf("source summary includes tool inventory: %s", search.Prompt())
	}
	for _, query := range []string{"read", "document_id", "shared documents"} {
		result := search.Execute(t.Context(), map[string]any{"query": query})
		if result.IsError || len(resultNames(t, result)) == 0 {
			t.Fatalf("%s: %+v", query, result)
		}
		var body struct {
			Tools []toolapi.Spec `json:"tools"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil {
			t.Fatal(err)
		}
		for _, spec := range body.Tools {
			if spec.Parameters["additionalProperties"] != false || len(spec.Parameters["required"].([]any)) != 1 {
				t.Fatalf("incomplete input schema: %+v", spec)
			}
		}
	}
	r := search.Execute(t.Context(), map[string]any{"query": "read", "limit": 1})
	if names := resultNames(t, r); !slices.Equal(names, []string{read.Name()}) {
		t.Fatalf("ranking %v", names)
	}
	if names := c.Names(); !slices.Equal(names, []string{read.Name(), write.Name()}) {
		t.Fatal(names)
	}
	r = search.Execute(t.Context(), map[string]any{"query": "nonexistentuniqueterm"})
	if r.IsError || len(resultNames(t, r)) != 0 {
		t.Fatalf("empty search %+v", r)
	}
}

func TestSearchArgumentsLimitsAndStableOrder(t *testing.T) {
	var tools []toolapi.Tool
	for i := 'a'; i <= 'z'; i++ {
		tools = append(tools, fixture("tool_"+string(i), "shared common operation"))
	}
	c := New(tools)
	for _, args := range []map[string]any{
		nil, {"query": ""}, {"query": " \n"}, {"query": 42}, {"query": strings.Repeat("x", maxQueryBytes+1)},
		{"query": "common", "limit": 0}, {"query": "common", "limit": 33}, {"query": "common", "limit": 1.5},
		{"query": "common", "limit": nil}, {"query": "common", "unknown": true},
	} {
		if result := c.SearchTool().Execute(t.Context(), args); !result.IsError {
			t.Fatalf("accepted %#v", args)
		}
	}
	for _, limit := range []any{1, int64(1), float64(1), json.Number("1")} {
		r := c.SearchTool().Execute(t.Context(), map[string]any{"query": "common", "limit": limit})
		if r.IsError || !slices.Equal(resultNames(t, r), []string{"tool_a"}) {
			t.Fatalf("%v: %+v", limit, r)
		}
	}
	if names := resultNames(t, c.SearchTool().Execute(t.Context(), map[string]any{"query": "common"})); len(names) != defaultLimit {
		t.Fatalf("default count %d", len(names))
	}
	slices.Reverse(tools)
	reordered := New(tools)
	a := c.SearchTool().Execute(t.Context(), map[string]any{"query": "common"})
	b := reordered.SearchTool().Execute(t.Context(), map[string]any{"query": "common"})
	if !slices.Equal(resultNames(t, a), resultNames(t, b)) {
		t.Fatalf("unstable order %v / %v", resultNames(t, a), resultNames(t, b))
	}
}

func TestSearchOutputBudgetDoesNotPublishPartialSchema(t *testing.T) {
	huge := fixture("huge_schema", "Uniquehuge operation")
	huge.params["properties"].(map[string]any)["large_enum"] = map[string]any{"type": "string", "enum": []any{strings.Repeat("x", maxResultBytes*2)}}
	c := New([]toolapi.Tool{huge})
	r := c.SearchTool().Execute(t.Context(), map[string]any{"query": "uniquehuge"})
	if r.IsError || len(resultNames(t, r)) != 0 || !strings.Contains(r.Content[0].Text, "omitted_tools") || len(r.Content[0].Text) > maxResultBytes {
		t.Fatalf("budget result: %+v", r)
	}
	if strings.Contains(r.Content[0].Text, "large_enum") {
		t.Fatal("partial schema leaked")
	}
}

func pairedHistory(id string, result toolapi.Result) []types.Message {
	return []types.Message{
		{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: id, Name: Name, Arguments: map[string]any{"query": "document"}}}},
		{Role: "toolResult", ToolCallID: id, ToolName: Name, IsError: result.IsError, Details: result.Details, Content: result.Content},
	}
}

func TestLoadedAcceptsOnlyPairedSuccessfulCurrentIdentities(t *testing.T) {
	read := fixture("mcp_docs_read", "Read documents")
	c := New([]toolapi.Tool{read})
	r := c.SearchTool().Execute(t.Context(), map[string]any{"query": "document"})
	history := pairedHistory("search", r)
	if got := c.Loaded(history); len(got) != 1 || got[0].Name() != read.Name() {
		t.Fatalf("loaded %v", got)
	}
	// Details cannot resurrect a schema that output policy did not disclose.
	history[1].Content = []types.Content{{Type: "text", Text: `{"tools":[{"name":"different","parameters":{}}]}`}}
	if got := c.Loaded(history); len(got) != 0 {
		t.Fatal(got)
	}
	cases := map[string]func([]types.Message) []types.Message{
		"orphan":          func(h []types.Message) []types.Message { return h[1:] },
		"error":           func(h []types.Message) []types.Message { h[1].IsError = true; return h },
		"wrongResultName": func(h []types.Message) []types.Message { h[1].ToolName = "other"; return h },
		"wrongCallID":     func(h []types.Message) []types.Message { h[1].ToolCallID = "other"; return h },
		"invalidCall": func(h []types.Message) []types.Message {
			h[0].Content[0].Arguments = map[string]any{"query": ""}
			return h
		},
		"userCall":        func(h []types.Message) []types.Message { h[0].Role = "user"; return h },
		"failedAssistant": func(h []types.Message) []types.Message { h[0].StopReason = "error"; return h },
		"missingDetails":  func(h []types.Message) []types.Message { h[1].Details = nil; return h },
		"redactedBody": func(h []types.Message) []types.Message {
			h[1].Content = []types.Content{{Type: "text", Text: "safe redacted output"}}
			return h
		},
		"partialSchema": func(h []types.Message) []types.Message {
			h[1].Content = []types.Content{{Type: "text", Text: `{"tools":[{"name":"mcp_docs_read","type":"function","parameters":{}}]}`}}
			return h
		},
		"removedTool": func(h []types.Message) []types.Message {
			h[1].Details = map[string]any{"discovered_tools": []string{"removed_tool"}}
			return h
		},
		"replayedErrorID": func(h []types.Message) []types.Message {
			failed := h[1]
			failed.IsError = true
			return []types.Message{h[0], failed, h[1]}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := pairedHistory("search", r)
			if got := c.Loaded(mutate(h)); len(got) != 0 {
				t.Fatalf("unsafe loading %+v", got)
			}
		})
	}
	if got := New(nil).Loaded(history); len(got) != 0 {
		t.Fatal("removed catalog retained identity")
	}
}

func TestCatalogFreezesSchemasButReconcilesNewCatalog(t *testing.T) {
	read := fixture("mcp_docs_read", "Read documents")
	c := New([]toolapi.Tool{read})
	r := c.SearchTool().Execute(t.Context(), map[string]any{"query": "document"})
	h := pairedHistory("search", r)
	read.params["type"] = "changed"
	if got := toolapi.SpecFor(c.Loaded(h)[0]).Parameters["type"]; got != "object" {
		t.Fatalf("snapshot mutated: %v", got)
	}
	next := New([]toolapi.Tool{read})
	if got := next.Loaded(h); len(got) != 0 {
		t.Fatal("stale schema body loaded a changed declaration")
	}
}

func TestCatalogExcludesRecursiveToolsAndBoundsUTF8Sources(t *testing.T) {
	c := New([]toolapi.Tool{fixture(Name, "search"), fixture("exec", "execute"), fixture("wait", "wait"), fixture("actual", "normal")})
	if !slices.Equal(c.Names(), []string{"actual"}) {
		t.Fatal(c.Names())
	}
	long := strings.Repeat("中文", 5000)
	if got := boundText(long, 256); !utf8.ValidString(got) || len(got) > 259 {
		t.Fatal("UTF-8 boundary broken")
	}
	c = New([]toolapi.Tool{&testTool{name: "actual", source: long, sourceDescription: long}})
	if summary := c.SourceSummary(); !utf8.ValidString(summary) || len(summary) > maxSourceBytes {
		t.Fatal("unbounded source summary")
	}
}
