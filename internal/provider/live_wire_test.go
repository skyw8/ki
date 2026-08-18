//go:build live

package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/types"
)

// Hits DashScope with two consecutive image tool results in one Completions
// turn — the packing that used to insert user between role:tool messages.
func TestLiveCompletionsAcceptsBatchedToolImages(t *testing.T) {
	model, key := loadLiveDashScope(t)

	red := solidPNG(t, color.RGBA{R: 200, G: 16, B: 16, A: 255})
	blue := solidPNG(t, color.RGBA{R: 16, G: 16, B: 200, A: 255})

	req := loop.Request{
		Model:    "qwen3.7-plus",
		Provider: "dashscope-cn",
		API:      "completions",
		System:   "You are a vision checker. Reply with one line COLOR_A=<word> COLOR_B=<word>.",
		Messages: []types.Message{
			{Role: "user", Content: []types.Content{{Type: "text", Text: "What color is each attached tool image? Do not guess."}}},
			{Role: "assistant", Content: []types.Content{
				{Type: "toolCall", ID: "call_a", Name: "Read", Arguments: map[string]any{"file_path": "/a.png"}},
				{Type: "toolCall", ID: "call_b", Name: "Read", Arguments: map[string]any{"file_path": "/b.png"}},
			}},
			{Role: "toolResult", ToolCallID: "call_a", ToolName: "Read", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: red, MIMEType: "image/png"},
			}},
			{Role: "toolResult", ToolCallID: "call_b", ToolName: "Read", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: blue, MIMEType: "image/png"},
			}},
		},
	}

	// Guard the wire shape before we spend a live call.
	body := CompletionsBody(req)
	var roles []string
	for _, m := range mustType[[]map[string]any](t, body["messages"]) {
		roles = append(roles, mustType[string](t, m["role"]))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool,user" {
		t.Fatalf("unexpected live wire roles: %s", strings.Join(roles, ","))
	}

	live := NewLiveModel(model, key, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	got, err := live.Stream(ctx, req, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatalf("stream: %v errMsg=%s", err, got.ErrorMessage)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("model error: %s", got.ErrorMessage)
	}
	text := strings.ToLower(got.Text())
	if !strings.Contains(text, "red") && !strings.Contains(text, "crimson") {
		t.Fatalf("missing red: %q", got.Text())
	}
	if !strings.Contains(text, "blue") && !strings.Contains(text, "azure") {
		t.Fatalf("missing blue: %q", got.Text())
	}
}

func solidPNG(t *testing.T, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func loadLiveDashScope(t *testing.T) (Model, string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	reg, err := NewRegistry(filepath.Join(home, ".ki"))
	if err != nil {
		t.Skip(err)
	}
	_, model, key, err := reg.Resolve("dashscope-cn", "qwen3.7-plus")
	if err != nil {
		t.Skip("no dashscope key")
	}
	return model, key
}
