package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	toolapi "ki/internal/tool"
	"ki/internal/types"
)

func TestPageCursorRejectsSplitUTF8AndPastEnd(t *testing.T) {
	x := fixture("utf8_tool", "UTF-8 declaration")
	x.params["description"] = strings.Repeat("中", 5000)
	c := New([]toolapi.Tool{x})
	raw := c.entries[0].raw
	split := strings.Index(string(raw), "中") + 1
	for _, offset := range []int{split, len(raw), len(raw) + 1} {
		cursor := fmt.Sprintf("%s:%d", declarationDigest(raw), offset)
		if result := c.SearchTool().Execute(t.Context(), map[string]any{"name": x.Name(), "cursor": cursor}); !result.IsError {
			t.Fatalf("accepted cursor offset %d", offset)
		}
	}
}

func TestLoadedRejectsRoundedIntegerSchemaRewrite(t *testing.T) {
	x := fixture("numeric_tool", "Numeric identifier")
	x.params["properties"].(map[string]any)["id"] = map[string]any{"const": json.Number("9007199254740992")}
	c := New([]toolapi.Tool{x})
	result := c.SearchTool().Execute(t.Context(), map[string]any{"query": "numeric"})
	text := strings.Replace(result.Content[0].Text, "9007199254740992", "9007199254740993", 1)
	if text == result.Content[0].Text {
		t.Fatal("fixture does not contain expected numeric constraint")
	}
	result.Content = []types.Content{{Type: "text", Text: text}}
	if loaded := c.Loaded(pairedHistory("rewritten_integer", result)); len(loaded) != 0 {
		t.Fatal("rounded integer accepted as complete schema")
	}
}
