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

func TestAnthropicBodyBatchesConsecutiveToolResults(t *testing.T) {
	body := AnthropicBody(loop.Request{
		Model: "claude-sonnet-4-5",
		Messages: []types.Message{
			{Role: "assistant", Content: []types.Content{
				{Type: "toolCall", ID: "c1", Name: "Read"},
				{Type: "toolCall", ID: "c2", Name: "Read"},
			}},
			{Role: "toolResult", ToolCallID: "c1", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: "AAA", MIMEType: "image/png"},
			}},
			{Role: "toolResult", ToolCallID: "c2", Content: []types.Content{
				{Type: "text", Text: "ok"},
			}},
		},
	})
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 2 {
		t.Fatalf("want assistant + one user, got %d: %+v", len(msgs), msgs)
	}
	if msgs[1]["role"] != "user" {
		t.Fatalf("last role: %+v", msgs[1])
	}
	blocks, ok := msgs[1]["content"].([]map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("tool_result blocks: %#v", msgs[1]["content"])
	}
	if blocks[0]["type"] != "tool_result" || blocks[1]["type"] != "tool_result" {
		t.Fatalf("blocks: %+v", blocks)
	}
	inner, ok := blocks[0]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("image tool_result should be blocks: %#v", blocks[0]["content"])
	}
	var kinds []string
	for _, p := range inner {
		kinds = append(kinds, fmt.Sprint(p["type"]))
	}
	if !strings.Contains(strings.Join(kinds, ","), "image") {
		t.Fatalf("missing image in tool_result: %v", kinds)
	}
}

func TestCompletionsBodyForwardsImages(t *testing.T) {
	body := CompletionsBody(loop.Request{
		Model: "qwen3.7-plus",
		Messages: []types.Message{
			{Role: "user", Content: []types.Content{
				{Type: "text", Text: "what color"},
				{Type: "image", Data: "AAA", MIMEType: "image/png"},
			}},
			{Role: "assistant", Content: []types.Content{
				{Type: "toolCall", ID: "c1", Name: "Read", Arguments: map[string]any{"file_path": "/a.png"}},
			}},
			{Role: "toolResult", ToolCallID: "c1", ToolName: "Read", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: "BBB", MIMEType: "image/png"},
			}},
		},
	})
	msgs := body["messages"].([]map[string]any)
	user := msgs[0]
	parts, ok := user["content"].([]map[string]any)
	if !ok {
		t.Fatalf("user content should be multimodal parts: %#v", user["content"])
	}
	if parts[0]["type"] != "text" {
		t.Fatalf("user text part: %+v", parts[0])
	}
	img := parts[1]["image_url"].(map[string]any)
	if img["url"] != "data:image/png;base64,AAA" {
		t.Fatalf("user image: %+v", img)
	}

	var sawTool, sawFollowup bool
	for _, m := range msgs {
		if m["role"] == "tool" {
			sawTool = true
			if m["content"] != "Read image file [image/png]" {
				t.Fatalf("tool text: %+v", m)
			}
		}
		if m["role"] != "user" {
			continue
		}
		extra, ok := m["content"].([]map[string]any)
		if !ok {
			continue
		}
		for _, p := range extra {
			if p["type"] != "image_url" {
				continue
			}
			u := p["image_url"].(map[string]any)
			if u["url"] == "data:image/png;base64,BBB" {
				sawFollowup = true
			}
		}
	}
	if !sawTool || !sawFollowup {
		t.Fatalf("tool=%v followup=%v msgs=%+v", sawTool, sawFollowup, msgs)
	}
}

func TestCompletionsBodyBatchesParallelToolImages(t *testing.T) {
	body := CompletionsBody(loop.Request{
		Model: "qwen3.7-plus",
		Messages: []types.Message{
			{Role: "user", Content: []types.Content{{Type: "text", Text: "colors"}}},
			{Role: "assistant", Content: []types.Content{
				{Type: "toolCall", ID: "c1", Name: "Read", Arguments: map[string]any{"file_path": "/a.png"}},
				{Type: "toolCall", ID: "c2", Name: "Read", Arguments: map[string]any{"file_path": "/b.png"}},
			}},
			{Role: "toolResult", ToolCallID: "c1", ToolName: "Read", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: "AAA", MIMEType: "image/png"},
			}},
			{Role: "toolResult", ToolCallID: "c2", ToolName: "Read", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: "BBB", MIMEType: "image/png"},
			}},
			{Role: "user", Content: []types.Content{{Type: "text", Text: "thanks"}}},
		},
	})
	var roles []string
	var followup []map[string]any
	for _, m := range body["messages"].([]map[string]any) {
		role, _ := m["role"].(string)
		roles = append(roles, role)
		if role == "user" {
			if parts, ok := m["content"].([]map[string]any); ok {
				for _, p := range parts {
					if p["type"] == "image_url" {
						followup = append(followup, p)
					}
				}
			}
		}
	}
	got := strings.Join(roles, ",")
	want := "user,assistant,tool,tool,user,user"
	if got != want {
		t.Fatalf("roles %s want %s\n%+v", got, want, body["messages"])
	}
	if len(followup) != 2 {
		t.Fatalf("expected 2 images in one followup, got %d: %+v", len(followup), followup)
	}
	u0 := followup[0]["image_url"].(map[string]any)["url"]
	u1 := followup[1]["image_url"].(map[string]any)["url"]
	if u0 != "data:image/png;base64,AAA" || u1 != "data:image/png;base64,BBB" {
		t.Fatalf("urls %v %v", u0, u1)
	}
}

func TestReplayableDropsAbortedAndEmptyAssistants(t *testing.T) {
	// tmp3: 你好 → abort (empty assistant) → 你好. Empty aborted assistant
	// must not be replayed or Completions/Anthropic 400.
	hist := []types.Message{
		{Role: "user", Content: []types.Content{{Type: "text", Text: "你好"}}},
		{Role: "assistant", StopReason: "aborted", ErrorMessage: "context canceled"},
		{Role: "user", Content: []types.Content{{Type: "text", Text: "你好"}}},
	}
	comp := CompletionsBody(loop.Request{Model: "deepseek-v4-flash", Messages: hist})
	var roles []string
	for _, m := range comp["messages"].([]map[string]any) {
		roles = append(roles, fmt.Sprint(m["role"]))
		if m["role"] == "assistant" {
			t.Fatalf("aborted assistant leaked into completions: %+v", m)
		}
	}
	if strings.Join(roles, ",") != "user,user" {
		t.Fatalf("completions roles %s", strings.Join(roles, ","))
	}

	anth := AnthropicBody(loop.Request{Model: "claude-sonnet-4-5", Messages: hist})
	roles = nil
	for _, m := range anth["messages"].([]map[string]any) {
		roles = append(roles, fmt.Sprint(m["role"]))
		if m["role"] == "assistant" {
			t.Fatalf("aborted assistant leaked into anthropic: %+v", m)
		}
	}
	if strings.Join(roles, ",") != "user,user" {
		t.Fatalf("anthropic roles %s", strings.Join(roles, ","))
	}

	resp := ResponsesBody(loop.Request{Model: "gpt-4o", Messages: hist})
	var kinds []string
	for _, raw := range resp["input"].([]any) {
		item := raw.(map[string]any)
		kinds = append(kinds, fmt.Sprintf("%s/%s", item["type"], item["role"]))
	}
	if strings.Join(kinds, ",") != "message/user,message/user" {
		t.Fatalf("responses items %s", strings.Join(kinds, ","))
	}
}

func TestReplayableDropsErrorEmptyAndOrphanToolResults(t *testing.T) {
	hist := []types.Message{
		{Role: "user", Content: []types.Content{{Type: "text", Text: "a"}}},
		{Role: "assistant", StopReason: "error", ErrorMessage: "400", Content: []types.Content{
			{Type: "toolCall", ID: "c1", Name: "Read"},
		}},
		{Role: "toolResult", ToolCallID: "c1", Content: []types.Content{{Type: "text", Text: "partial"}}},
		{Role: "assistant"}, // empty, no stopReason
		{Role: "user", Content: []types.Content{{Type: "text", Text: "b"}}},
	}
	got := replayable(hist)
	if len(got) != 2 || got[0].Role != "user" || got[1].Role != "user" {
		t.Fatalf("want two users, got %+v", got)
	}
}

func TestReplayableSynthesizesMissingToolResults(t *testing.T) {
	hist := []types.Message{
		{Role: "user", Content: []types.Content{{Type: "text", Text: "a"}}},
		{Role: "assistant", Content: []types.Content{
			{Type: "toolCall", ID: "c1", Name: "Read"},
			{Type: "toolCall", ID: "c2", Name: "Read"},
		}},
		{Role: "toolResult", ToolCallID: "c1", Content: []types.Content{{Type: "text", Text: "ok"}}},
		{Role: "user", Content: []types.Content{{Type: "text", Text: "b"}}},
	}
	got := replayable(hist)
	if len(got) != 5 {
		t.Fatalf("len %d: %+v", len(got), got)
	}
	if got[3].Role != "toolResult" || got[3].ToolCallID != "c2" || !got[3].IsError {
		t.Fatalf("synthetic: %+v", got[3])
	}
	if got[3].Text() != "No result provided" {
		t.Fatalf("synthetic text: %q", got[3].Text())
	}
}

func TestReplayableKeepsThinkingOnlyAssistant(t *testing.T) {
	hist := []types.Message{
		{Role: "user", Content: []types.Content{{Type: "text", Text: "a"}}},
		{Role: "assistant", Content: []types.Content{{Type: "thinking", Thinking: "hmm"}}},
		{Role: "user", Content: []types.Content{{Type: "text", Text: "b"}}},
	}
	got := replayable(hist)
	if len(got) != 3 || got[1].Role != "assistant" {
		t.Fatalf("thinking assistant dropped: %+v", got)
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

func TestResponsesBodyEmbedsToolImages(t *testing.T) {
	body := ResponsesBody(loop.Request{
		Model: "gpt-4o",
		Messages: []types.Message{
			{Role: "toolResult", ToolCallID: "c1", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: "AAA", MIMEType: "image/png"},
			}},
			{Role: "toolResult", ToolCallID: "c2", Content: []types.Content{
				{Type: "text", Text: "Read image file [image/png]"},
				{Type: "image", Data: "BBB", MIMEType: "image/png"},
			}},
		},
	})
	input := body["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("want 2 outputs, no extra user, got %d: %+v", len(input), input)
	}
	for i, raw := range input {
		item := raw.(map[string]any)
		if item["type"] != "function_call_output" {
			t.Fatalf("item %d: %+v", i, item)
		}
		out, ok := item["output"].([]map[string]any)
		if !ok {
			t.Fatalf("output should be multimodal array: %#v", item["output"])
		}
		var types []string
		for _, p := range out {
			types = append(types, fmt.Sprint(p["type"]))
		}
		joined := strings.Join(types, ",")
		if !strings.Contains(joined, "input_text") || !strings.Contains(joined, "input_image") {
			t.Fatalf("item %d parts: %s", i, joined)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestUsageParsingPerProtocol(t *testing.T) {
	// Completions: prompt_cache_hit_tokens → CacheRead; TotalTokens is the
	// four-part sum computed by the streamer.
	t.Run("completions", func(t *testing.T) {
		sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_cache_hit_tokens\":30}}\n\n" +
			"data: [DONE]\n"
		live := NewLive("completions", "https://api.deepseek.com", "sk", roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(sse)), Header: make(http.Header)}, nil
		}))
		m, err := live.Stream(context.Background(), loop.Request{Model: "x", Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hey"}}}}}, func(loop.AssistantDelta) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		u := m.Usage
		if u.Input != 100 || u.Output != 20 || u.CacheRead != 30 || u.TotalTokens != 150 {
			t.Fatalf("completions usage: %+v", u)
		}
	})
	// Responses: cached_tokens → CacheRead, total_tokens comes straight from
	// the API (may not equal the four-part sum).
	t.Run("responses", func(t *testing.T) {
		sse := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"cached_tokens\":30,\"total_tokens\":150}}}\n\n" +
			"event: response.completed\ndata: [DONE]\n"
		live := NewLive("responses", "https://api.openai.com/v1", "sk", roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(sse)), Header: make(http.Header)}, nil
		}))
		m, err := live.Stream(context.Background(), loop.Request{Model: "x", Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hey"}}}}}, func(loop.AssistantDelta) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		u := m.Usage
		if u.Input != 100 || u.Output != 20 || u.CacheRead != 30 || u.TotalTokens != 150 {
			t.Fatalf("responses usage: %+v", u)
		}
	})
	// Anthropic: cache_read_input_tokens on message_start + message_delta;
	// TotalTokens is the four-part sum.
	t.Run("anthropic", func(t *testing.T) {
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":30,\"cache_creation_input_tokens\":5}}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20,\"cache_read_input_tokens\":30,\"cache_creation_input_tokens\":5}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		live := NewLive("anthropic", "https://api.anthropic.com", "sk", roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(sse)), Header: make(http.Header)}, nil
		}))
		m, err := live.Stream(context.Background(), loop.Request{Model: "x", Messages: []types.Message{{Role: "user", Content: []types.Content{{Type: "text", Text: "hey"}}}}}, func(loop.AssistantDelta) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		u := m.Usage
		if u.Input != 100 || u.Output != 20 || u.CacheRead != 30 || u.CacheWrite != 5 || u.TotalTokens != 155 {
			t.Fatalf("anthropic usage: %+v", u)
		}
	})
}

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
