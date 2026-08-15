package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/types"
)

func TestCompletionsBodyShapesToolsAndRoles(t *testing.T) {
	body := CompletionsBody(loop.Request{
		System: "sys",
		Model:  "deepseek-chat",
		Messages: []types.Message{
			{Role: "user", Content: []types.Content{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []types.Content{
				{Type: "text", Text: "call"},
				{Type: "toolCall", ID: "c1", Name: "Read", Arguments: map[string]any{"file_path": "/a"}},
			}},
			{Role: "toolResult", ToolCallID: "c1", Content: []types.Content{{Type: "text", Text: "data"}}},
		},
		Tools: []loop.ToolSpec{{
			Name:        "Read",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		}},
	})
	if body["model"] != "deepseek-chat" {
		t.Fatalf("model: %v", body["model"])
	}
	msgs := body["messages"].([]map[string]any)
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "sys" {
		t.Fatalf("system: %+v", msgs[0])
	}
	if msgs[len(msgs)-1]["role"] != "tool" {
		t.Fatalf("tool result role: %+v", msgs[len(msgs)-1])
	}
	tools := body["tools"].([]map[string]any)
	fn := tools[0]["function"].(map[string]any)
	if fn["name"] != "Read" {
		t.Fatalf("tool name: %v", fn["name"])
	}
}

func TestAnthropicBodyUsesCacheControlAndToolUse(t *testing.T) {
	body := AnthropicBody(loop.Request{
		System: "layered",
		Model:  "claude-sonnet-4-5",
		Messages: []types.Message{
			{Role: "user", Content: []types.Content{{Type: "text", Text: "x"}}},
		},
		Tools: []loop.ToolSpec{{Name: "Bash", Description: "sh", Parameters: map[string]any{"type": "object"}}},
	})
	sys := body["system"].([]map[string]any)
	if sys[0]["text"] != "layered" {
		t.Fatalf("system: %+v", sys[0])
	}
	if _, ok := sys[0]["cache_control"]; !ok {
		t.Fatal("expected cache_control on system")
	}
	tools := body["tools"].([]map[string]any)
	if tools[0]["name"] != "Bash" {
		t.Fatalf("tool: %+v", tools[0])
	}
	if _, ok := tools[0]["input_schema"]; !ok {
		t.Fatal("input_schema")
	}
}

func TestResponsesBodyUsesInstructions(t *testing.T) {
	body := ResponsesBody(loop.Request{System: "S", Model: "gpt-4o", Messages: []types.Message{
		{Role: "user", Content: []types.Content{{Type: "text", Text: "q"}}},
	}})
	if body["instructions"] != "S" {
		t.Fatalf("instructions: %v", body["instructions"])
	}
	if body["model"] != "gpt-4o" {
		t.Fatalf("model: %v", body["model"])
	}
	input := body["input"].([]any)
	first := input[0].(map[string]any)
	if first["type"] != "message" || first["role"] != "user" {
		t.Fatalf("user item: %+v", first)
	}
}

func TestResponsesBodyUsesFunctionCallOutput(t *testing.T) {
	body := ResponsesBody(loop.Request{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "user", Content: []types.Content{{Type: "text", Text: "write"}}},
			{Role: "assistant", Content: []types.Content{
				{Type: "toolCall", ID: "call_1", ItemID: "fc_1", Name: "Write", Arguments: map[string]any{"file_path": "/a"}},
			}},
			{Role: "toolResult", ToolCallID: "call_1", ToolName: "Write", Content: []types.Content{{Type: "text", Text: "ok"}}},
		},
	})
	input := body["input"].([]any)
	var kinds []string
	for _, raw := range input {
		item := raw.(map[string]any)
		kinds = append(kinds, fmt.Sprint(item["type"]))
		if item["type"] == "function_call" {
			if item["call_id"] != "call_1" || item["name"] != "Write" || item["id"] != "fc_1" {
				t.Fatalf("function_call: %+v", item)
			}
		}
		if item["type"] == "function_call_output" {
			if item["call_id"] != "call_1" || item["output"] != "ok" {
				t.Fatalf("function_call_output: %+v", item)
			}
		}
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "function_call") || !strings.Contains(joined, "function_call_output") {
		t.Fatalf("items: %s", joined)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestLiveCompletionsPostsChatCompletions(t *testing.T) {
	var gotURL string
	var gotAuth string
	var gotBody map[string]any
	doer := roundTrip(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n"
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewBufferString(sse)),
			Header:     make(http.Header),
		}, nil
	})
	live := NewLive("completions", "https://api.deepseek.com", "sk-test", doer)
	m, err := live.Stream(context.Background(), loop.Request{
		Model:    "deepseek-chat",
		Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hey"}}}},
	}, func(loop.AssistantDelta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("url: %s", gotURL)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth: %s", gotAuth)
	}
	if m.Text() != "hi" {
		t.Fatalf("text: %q", m.Text())
	}
}

func TestResolvePrefersSessionThenTomlThenFirstKey(t *testing.T) {
	cfg := config.Builtin(t.TempDir())
	cfg.Providers = map[string]config.Provider{
		"openai":    {APIKey: "o"},
		"anthropic": {APIKey: "a"},
	}
	cfg.Defaults = config.Defaults{Provider: "openai", Model: "gpt-4o"}
	p, m := Resolve(cfg, "anthropic", "claude-sonnet-4-5", "")
	if p != "anthropic" || m != "claude-sonnet-4-5" {
		t.Fatalf("session win: %s/%s", p, m)
	}
	p, m = Resolve(cfg, "", "", "zhipu-cn/glm-4.5")
	if p != "zhipu-cn" || m != "glm-4.5" {
		t.Fatalf("explicit: %s/%s", p, m)
	}
	p, m = Resolve(cfg, "", "", "")
	if p != "openai" || m != "gpt-4o" {
		t.Fatalf("toml default: %s/%s", p, m)
	}
	empty := config.Builtin(t.TempDir())
	p, m = Resolve(empty, "", "", "")
	if p == "" || m == "" {
		t.Fatalf("fallback empty: %s/%s", p, m)
	}
}
