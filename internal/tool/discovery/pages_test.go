package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

func declarationPages(t *testing.T, c *Catalog, name string) ([]schemaPage, []types.Message) {
	t.Helper()
	var pages []schemaPage
	var history []types.Message
	args := map[string]any{"name": name}
	for i := 0; ; i++ {
		if i > 256 {
			t.Fatal("pagination did not terminate")
		}
		result := c.SearchTool().Execute(t.Context(), args)
		if result.IsError || len(result.Content) != 1 {
			t.Fatalf("page %d: %+v", i, result)
		}
		text := result.Content[0].Text
		if len(text) > maxResultBytes || !utf8.ValidString(text) {
			t.Fatalf("page %d exceeds UTF-8 output budget: %d", i, len(text))
		}
		var body struct {
			Page schemaPage `json:"schema_page"`
		}
		if err := json.Unmarshal([]byte(text), &body); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(body.Page.Fragment) || body.Page.Fragment == "" {
			t.Fatalf("invalid fragment at page %d", i)
		}
		id := fmt.Sprintf("page-%d", i)
		history = append(history,
			types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: id, Name: Name, Arguments: args}}},
			types.Message{Role: "toolResult", ToolCallID: id, ToolName: Name, Content: result.Content},
		)
		pages = append(pages, body.Page)
		if body.Page.NextCursor == "" {
			return pages, history
		}
		args = map[string]any{"name": name, "cursor": body.Page.NextCursor}
	}
}

func TestDeclarationPagesCompleteUTF8AndEscapedJSON(t *testing.T) {
	for _, value := range []string{strings.Repeat("x", 256*1024-1024), strings.Repeat("中文é", 20000), strings.Repeat("\\\"\n<>&", 5000)} {
		huge := fixture("large_tool", "Large unique declaration")
		huge.params["description"] = value
		c := New([]toolapi.Tool{huge})
		search := c.SearchTool().Execute(t.Context(), map[string]any{"query": "unique"})
		if len(c.Loaded(pairedHistory("search", search))) != 0 || !strings.Contains(search.Content[0].Text, `"schema_pages":[{"name":"large_tool"}]`) {
			t.Fatalf("large declaration must supply a page entry: %+v", search)
		}
		pages, history := declarationPages(t, c, huge.Name())
		var complete strings.Builder
		for i, page := range pages {
			if page.Offset != complete.Len() || page.Digest != pages[0].Digest {
				t.Fatalf("noncontiguous page %d: %+v", i, page)
			}
			complete.WriteString(page.Fragment)
			if (i == 0 || i == len(pages)/2 || i == len(pages)-2) && len(c.Loaded(history[:(i+1)*2])) != 0 {
				t.Fatalf("activated before complete delivery at page %d", i)
			}
		}
		if complete.String() != string(c.entries[0].raw) || pages[len(pages)-1].TotalBytes != complete.Len() {
			t.Fatal("pages did not reconstruct the complete current declaration")
		}
		if got := c.Loaded(history); len(got) != 1 || got[0].Name() != huge.Name() {
			t.Fatalf("complete delivery not loaded: %v", got)
		}
	}
}

func TestDeclarationPagesRejectIncompleteStaleAndRewrittenHistory(t *testing.T) {
	huge := fixture("large_tool", "Large declaration")
	huge.params["description"] = strings.Repeat("wide", 16000)
	c := New([]toolapi.Tool{huge})
	pages, original := declarationPages(t, c, huge.Name())
	if len(pages) < 3 {
		t.Fatal("fixture must span at least three pages")
	}
	cases := map[string]func([]types.Message) []types.Message{
		"lastPageOnly":  func(h []types.Message) []types.Message { return h[len(h)-2:] },
		"missingMiddle": func(h []types.Message) []types.Message { return append(h[:2:2], h[4:]...) },
		"reordered": func(h []types.Message) []types.Message {
			return append(append(slices.Clone(h[2:4]), h[:2]...), h[4:]...)
		},
		"orphanFirst": func(h []types.Message) []types.Message { return h[1:] },
		"failedFirst": func(h []types.Message) []types.Message { h[1].IsError = true; return h },
		"redactedFirst": func(h []types.Message) []types.Message {
			h[1].Content = []types.Content{{Type: "text", Text: "redacted"}}
			return h
		},
		"forgedDetails": func(h []types.Message) []types.Message {
			h[1].Content = nil
			h[1].Details = map[string]any{"discovered_tools": []string{huge.Name()}}
			return h
		},
		"wrongDigest": func(h []types.Message) []types.Message {
			page := pages[0]
			page.Digest = strings.Repeat("0", 64)
			raw, _ := json.Marshal(map[string]any{"schema_page": page})
			h[1].Content = []types.Content{{Type: "text", Text: string(raw)}}
			return h
		},
		"rewrittenFragment": func(h []types.Message) []types.Message {
			page := pages[0]
			page.Fragment = strings.Replace(page.Fragment, "Large", "Other", 1)
			raw, _ := json.Marshal(map[string]any{"schema_page": page})
			h[1].Content = []types.Content{{Type: "text", Text: string(raw)}}
			return h
		},
		"forgedCompletion": func(h []types.Message) []types.Message {
			page := pages[0]
			page.TotalBytes = len(page.Fragment)
			page.NextCursor = ""
			raw, _ := json.Marshal(map[string]any{"schema_page": page})
			h[1].Content = []types.Content{{Type: "text", Text: string(raw)}}
			return h
		},
		"wrongCallArguments": func(h []types.Message) []types.Message {
			h[0].Content = []types.Content{{Type: "toolCall", ID: "page-0", Name: Name, Arguments: map[string]any{"name": "other_tool"}}}
			return h
		},
		"replayedFailedID": func(h []types.Message) []types.Message {
			failed := h[1]
			failed.IsError = true
			return append(append([]types.Message{h[0], failed}, h[:2]...), h[2:]...)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.Loaded(mutate(slices.Clone(original))); len(got) != 0 {
				t.Fatalf("unsafe activation: %v", got)
			}
		})
	}
	if got := New(nil).Loaded(original); len(got) != 0 {
		t.Fatal("disabled tool reactivated")
	}
	huge.params["description"] = strings.Repeat("next", 16000)
	next := New([]toolapi.Tool{huge})
	if got := next.Loaded(original); len(got) != 0 {
		t.Fatal("old declaration activated changed catalog")
	}
	if r := next.SearchTool().Execute(t.Context(), map[string]any{"name": huge.Name(), "cursor": pages[0].NextCursor}); !r.IsError {
		t.Fatal("stale cursor accepted")
	}
}

func TestDeclarationPageArgumentsAndBoundedSearchHints(t *testing.T) {
	tool := fixture("known_tool", "known")
	c := New([]toolapi.Tool{tool})
	for _, args := range []map[string]any{
		{"name": "known_tool", "query": "known"}, {"name": "known_tool", "limit": 1},
		{"name": ""}, {"name": 42}, {"name": "KnownTool"}, {"name": "known_tool", "cursor": nil},
		{"name": "known_tool", "cursor": ""}, {"name": "known_tool", "cursor": "invalid"},
		{"name": "known_tool", "cursor": strings.Repeat("a", 64) + ":0"},
		{"name": "known_tool", "cursor": strings.Repeat("a", 64) + ":01"},
		{"name": "unknown_tool"}, {"name": "known_tool", "unknown": true},
	} {
		if result := c.SearchTool().Execute(t.Context(), args); !result.IsError {
			t.Fatalf("accepted invalid page arguments: %#v", args)
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if result := c.SearchTool().Execute(cancelled, map[string]any{"name": "known_tool"}); !result.IsError {
		t.Fatal("ignored cancellation")
	}
	var tools []toolapi.Tool
	for i := 0; i < maxLimit; i++ {
		x := fixture(fmt.Sprintf("tool_%02d_%s", i, strings.Repeat("a", 240)), "shared")
		x.params["description"] = strings.Repeat("x", maxResultBytes)
		tools = append(tools, x)
	}
	c = New(tools)
	result := c.SearchTool().Execute(t.Context(), map[string]any{"query": "shared", "limit": maxLimit})
	if result.IsError || len(result.Content[0].Text) > maxResultBytes {
		t.Fatal("unbounded page hints")
	}
	var body struct {
		Pages []map[string]string `json:"schema_pages"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil || len(body.Pages) != maxLimit {
		t.Fatalf("missing page hints: %v %+v", err, body)
	}
}
