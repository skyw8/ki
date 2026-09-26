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

// DeepSeek serves every wire protocol Ki speaks from one credential:
// Completions and Responses share the OpenAI-compatible root, Anthropic lives
// under /anthropic. A single live run therefore proves each protocol adapter can
// stream a real tool call and then replay its result — the second round whose
// tool shape used to 400 when it was encoded wrongly.
const (
	liveDeepSeekModel     = "deepseek-flash"
	liveDeepSeekBase      = "https://api.deepseek.com"
	liveDeepSeekAnthropic = liveDeepSeekBase + "/anthropic"
	liveTextMarker        = "KI-LIVE-MARKER-42"
)

type liveProtocol struct {
	name string
	api  string
	base string
}

func liveProtocols() []liveProtocol {
	return []liveProtocol{
		{name: "completions", api: "completions", base: liveDeepSeekBase},
		{name: "responses", api: "responses", base: liveDeepSeekBase},
		{name: "anthropic", api: "anthropic", base: liveDeepSeekAnthropic},
	}
}

// TestLiveDeepSeekReplaysToolResult runs a real tool call and feeds its result
// back, so the model must read the replayed result on each protocol's wire
// encoding. DeepSeek's thinking mode only accepts a replayed assistant turn that
// still carries the reasoning the model produced, which is exactly what the loop
// stores and replays.
func TestLiveDeepSeekReplaysToolResult(t *testing.T) {
	model, key := loadLiveDeepSeek(t)
	tools := []loop.ToolSpec{{
		Name: "lookup", Description: "Look up a marker value by key.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"key": map[string]any{"type": "string"}},
			"required":   []any{"key"},
		},
	}}
	user := types.Message{Role: "user", Content: []types.Content{
		{Type: "text", Text: "Call lookup with key=marker, then report the value it returned."},
	}}
	for _, proto := range liveProtocols() {
		t.Run(proto.name, func(t *testing.T) {
			live := newLiveProtocol(model, key, proto)
			system := "Call the lookup tool when asked. After a tool result, reply with exactly the marker value it contains and nothing else."
			first := streamLive(t, live, loop.Request{
				Model: liveDeepSeekModel, Provider: model.Provider, API: proto.api, MaxTokens: 1024,
				System: system, Tools: tools, Messages: []types.Message{user},
			})
			calls := toolCalls(first)
			if len(calls) != 1 || calls[0].Name != "lookup" {
				t.Fatalf("%s round 1: want one lookup call, got %d calls in %q", proto.name, len(calls), first.Text())
			}
			second := streamLive(t, live, loop.Request{
				Model: liveDeepSeekModel, Provider: model.Provider, API: proto.api, MaxTokens: 1024,
				System: system, Tools: tools,
				Messages: []types.Message{user, first, {
					Role: "toolResult", ToolCallID: calls[0].ID, ToolName: "lookup",
					Content: []types.Content{{Type: "text", Text: "marker = " + liveTextMarker}},
				}},
			})
			if !strings.Contains(second.Text(), liveTextMarker) {
				t.Fatalf("%s round 2 did not replay the tool result: %q", proto.name, second.Text())
			}
		})
	}
}

// TestLiveDeepSeekToolImages calls Read twice in one turn, then returns two image
// tool results — the packing that used to insert a user message between
// role:tool entries — and checks that each protocol places the images where its
// API allows them.
func TestLiveDeepSeekToolImages(t *testing.T) {
	model, key := loadLiveDeepSeek(t)
	tools := []loop.ToolSpec{{
		Name: "Read", Description: "Read a file from the workspace.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
			"required":   []any{"file_path"},
		},
	}}
	user := types.Message{Role: "user", Content: []types.Content{
		{Type: "text", Text: "Use the Read tool on both /a.png and /b.png. Make one Read call per file, in a single turn."},
	}}
	system := "You are a vision checker. The two image tool results are A then B, one pure red and one pure blue. Reply with exactly one line, either COLOR_A=red COLOR_B=blue or COLOR_A=blue COLOR_B=red, and nothing else."
	red := solidPNG(t, color.RGBA{R: 255, A: 255})
	blue := solidPNG(t, color.RGBA{B: 255, A: 255})
	for _, proto := range liveProtocols() {
		t.Run(proto.name, func(t *testing.T) {
			live := newLiveProtocol(model, key, proto)
			first := streamLive(t, live, loop.Request{
				Model: liveDeepSeekModel, Provider: model.Provider, API: proto.api, MaxTokens: 1024,
				System: system, Tools: tools, Messages: []types.Message{user},
			})
			calls := toolCalls(first)
			if len(calls) != 2 {
				t.Fatalf("%s round 1: want two Read calls, got %d in %q", proto.name, len(calls), first.Text())
			}
			if proto.api == "completions" {
				// Guard the image follow-up shape before we spend a second live call.
				roles := completionsRoles(t, []types.Message{user, first, toolResultWithImage(calls[0].ID, red), toolResultWithImage(calls[1].ID, blue)})
				if got := strings.Join(roles, ","); got != "system,user,assistant,tool,tool,user" {
					t.Fatalf("unexpected completions wire roles: %s", got)
				}
			}
			second := streamLive(t, live, loop.Request{
				Model: liveDeepSeekModel, Provider: model.Provider, API: proto.api, MaxTokens: 1024,
				System: system, Tools: tools,
				Messages: []types.Message{
					user, first,
					toolResultWithImage(calls[0].ID, red),
					toolResultWithImage(calls[1].ID, blue),
				},
			})
			// A is the red image and B the blue one, so the only correct answer is
			// this exact line; anything else means an image was dropped or swapped.
			if got := strings.ToUpper(strings.TrimSpace(second.Text())); !strings.Contains(got, "COLOR_A=RED") || !strings.Contains(got, "COLOR_B=BLUE") {
				t.Fatalf("%s did not report A=red B=blue: %q", proto.name, second.Text())
			}
		})
	}
}

func toolResultWithImage(callID, data string) types.Message {
	return types.Message{
		Role: "toolResult", ToolCallID: callID, ToolName: "Read",
		Content: []types.Content{
			{Type: "text", Text: "Read image file [image/png]"},
			{Type: "image", Data: data, MIMEType: "image/png"},
		},
	}
}

func completionsRoles(t *testing.T, messages []types.Message) []string {
	t.Helper()
	body := CompletionsBody(loop.Request{
		Model: liveDeepSeekModel, Provider: "deepseek", API: "completions", System: "system", Messages: messages,
	})
	var roles []string
	for _, m := range mustType[[]map[string]any](t, body["messages"]) {
		roles = append(roles, mustType[string](t, m["role"]))
	}
	return roles
}

func toolCalls(m types.Message) []types.Content {
	var out []types.Content
	for _, c := range m.Content {
		if c.Type == "toolCall" && c.ID != "" && c.Name != "" {
			out = append(out, c)
		}
	}
	return out
}

func newLiveProtocol(model Model, key string, proto liveProtocol) *Live {
	model.API = proto.api
	model.BaseURL = proto.base
	return NewLiveModel(model, key, nil)
}

func streamLive(t *testing.T, live *Live, req loop.Request) types.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	got, err := live.Stream(ctx, req, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatalf("%s stream: %v errMsg=%s", req.API, err, got.ErrorMessage)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("%s model error: %s", req.API, got.ErrorMessage)
	}
	return got
}

func solidPNG(t *testing.T, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	for y := range 128 {
		for x := range 128 {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func loadLiveDeepSeek(t *testing.T) (Model, string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	reg, err := NewRegistry(filepath.Join(home, ".ki"))
	if err != nil {
		t.Skip(err)
	}
	_, model, key, err := reg.Resolve("deepseek", liveDeepSeekModel)
	if err != nil {
		t.Skip("no deepseek key; set DEEPSEEK_API_KEY or configure it in Ki settings")
	}
	return model, key
}
