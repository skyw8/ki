package extension

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"ki/internal/loop"
	"ki/internal/types"
)

type blockWrite struct{ NopInterceptor }

func (blockWrite) BeforeTool(_ context.Context, in ToolCall) (ToolCall, *Block, error) {
	path, _ := in.Args["path"].(string)
	if in.Name == "Write" && strings.Contains(path, ".env") {
		return in, &Block{Reason: "blocked .env"}, nil
	}
	return in, nil, nil
}

type boom struct{ NopInterceptor }

func (boom) BeforeTool(context.Context, ToolCall) (ToolCall, *Block, error) {
	return ToolCall{}, nil, errRPC
}

func TestComposeBeforeToolBlockSkipsExecuteSemantics(t *testing.T) {
	hooks := ComposeHooks([]namedInterceptor{{
		name: "protected-paths", points: []string{InterceptTool}, inner: blockWrite{},
	}}, nil)
	args, block, reason, _, err := hooks.BeforeTool(context.Background(), "Write", map[string]any{"path": "/tmp/.env"})
	if err != nil || !block || reason != "blocked .env" {
		t.Fatalf("args=%v block=%v reason=%q err=%v", args, block, reason, err)
	}
	_, block, _, _, err = hooks.BeforeTool(context.Background(), "Read", map[string]any{"path": "/tmp/.env"})
	if err != nil || block {
		t.Fatal("read should pass")
	}
}

func TestComposeManagerErrorIsFailOpen(t *testing.T) {
	var saw string
	hooks := ComposeHooks([]namedInterceptor{{
		name: "bad", points: []string{InterceptTool}, inner: boom{},
	}}, func(name, cap, code, message string) { saw = name })
	_, block, _, _, err := hooks.BeforeTool(context.Background(), "Write", map[string]any{"path": "a"})
	if err != nil || block || saw != "bad" {
		t.Fatalf("fail-open err=%v block=%v saw=%q", err, block, saw)
	}
}

func TestComposeFailClosedBlocks(t *testing.T) {
	hooks := ComposeHooks([]namedInterceptor{{
		name: "bad", failClosed: true, points: []string{InterceptTool}, inner: boom{},
	}}, nil)
	_, block, reason, _, err := hooks.BeforeTool(context.Background(), "Write", map[string]any{})
	if err != nil || !block || !strings.Contains(reason, "failed closed") {
		t.Fatalf("failClosed %v %v %q", err, block, reason)
	}
}

func TestUndeclaredProviderPointNotInvoked(t *testing.T) {
	called := false
	inner := spyBeforeRun{fn: func() { called = true }}
	hooks := ComposeHooks([]namedInterceptor{{
		name: "onlytool", points: []string{InterceptTool}, inner: inner,
	}}, nil)
	_, _, err := hooks.BeforeRun(context.Background(), "sys", nil)
	if err != nil || called {
		t.Fatalf("provider point invoked: called=%v err=%v", called, err)
	}
}

type spyBeforeRun struct {
	NopInterceptor
	fn func()
}

func (s spyBeforeRun) BeforeRun(_ context.Context, system string, msgs []types.Message) (string, []types.Message, error) {
	s.fn()
	return system, msgs, nil
}

func TestHTTPViewStripsBodyAndKeys(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1", strings.NewReader(`{"secret":1}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer abc")
	req.Header.Set("X-Api-Key", "sk")
	req.Header.Set("Cookie", "sid=secret")
	req.Header.Set("X-Custom", "ok")
	view := viewHTTP(req)
	if view.Headers["Authorization"] != "" || view.Headers["X-Api-Key"] != "" || view.Headers["Cookie"] != "" {
		t.Fatalf("keys leaked: %+v", view.Headers)
	}
	if view.Headers["X-Custom"] != "ok" {
		t.Fatalf("custom: %+v", view.Headers)
	}
}

type captureDoer struct{ req *http.Request }

func (c *captureDoer) Do(req *http.Request) (*http.Response, error) {
	c.req = req
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
}

type emptyHeaderPatch struct{ NopInterceptor }

func (emptyHeaderPatch) BeforeProviderHTTP(_ context.Context, _ HTTPRequestView) (HTTPRequestPatch, error) {
	return HTTPRequestPatch{Headers: map[string]string{"X-Custom": ""}}, nil
}

func TestHTTPPatchEmptyHeaderDeletes(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Custom", "keep")
	cap := &captureDoer{}
	d := wrapHTTPDoer(cap, []namedInterceptor{{
		name: "h", points: []string{InterceptProviderHTTP}, inner: emptyHeaderPatch{},
	}}, nil, nil)
	_, err = d.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cap.req.Header.Get("X-Custom") != "" {
		t.Fatalf("expected delete, got %q", cap.req.Header.Get("X-Custom"))
	}
	if _, ok := cap.req.Header["X-Custom"]; ok {
		t.Fatal("empty patch must Del, not Set empty")
	}
}

type captureStreamer struct{ got loop.Request }

func (c *captureStreamer) Stream(_ context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	c.got = req
	return types.Message{Role: "assistant", StopReason: "stop"}, nil
}

type mutateModel struct{ NopInterceptor }

func (mutateModel) BeforeProvider(_ context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error) {
	req.Model = "other"
	req.Provider = "p2"
	return req, nil, nil
}

func TestOccupyStreamerKeepsImageDataAndCopiesModel(t *testing.T) {
	inner := &captureStreamer{}
	s := wrapStreamer(inner, []namedInterceptor{{
		name: "p", points: []string{InterceptProvider}, inner: mutateModel{},
	}}, nil, nil)
	orig := []types.Message{{
		Role: "user",
		Content: []types.Content{
			{Type: "text", Text: "hi"},
			{Type: "image", Data: "imagedata", MIMEType: "image/png"},
		},
	}}
	_, err := s.Stream(context.Background(), loop.Request{
		Provider: "p1", Model: "m1", Messages: orig,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inner.got.Model != "other" || inner.got.Provider != "p2" {
		t.Fatalf("mutations not copied: %+v", inner.got)
	}
	if len(inner.got.Messages) != 1 || len(inner.got.Messages[0].Content) < 2 {
		t.Fatalf("messages %+v", inner.got.Messages)
	}
	if inner.got.Messages[0].Content[1].Data != "imagedata" {
		t.Fatalf("image data stripped: %+v", inner.got.Messages[0].Content[1])
	}
}

func TestOccupyWideSkipSharedWithStreamer(t *testing.T) {
	skipped := newSkipSet()
	called := false
	hooks := composeHooks([]namedInterceptor{{
		name: "bad", points: []string{InterceptTool, InterceptProvider}, inner: boom{},
	}}, skipped, nil)
	_, block, _, _, err := hooks.BeforeTool(context.Background(), "Write", map[string]any{})
	if err != nil || block {
		t.Fatalf("fail-open err=%v block=%v", err, block)
	}
	inner := spyBeforeProvider{fn: func() { called = true }}
	s := wrapStreamer(&captureStreamer{}, []namedInterceptor{{
		name: "bad", points: []string{InterceptProvider}, inner: inner,
	}}, skipped, nil)
	_, err = s.Stream(context.Background(), loop.Request{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("BeforeProvider ran after BeforeTool skip")
	}
}

type spyBeforeProvider struct {
	NopInterceptor
	fn func()
}

func (s spyBeforeProvider) BeforeProvider(_ context.Context, req ProviderRequest) (ProviderRequest, *ShortCircuit, error) {
	s.fn()
	return req, nil, nil
}

func TestAfterToolErrorIgnoredByLoopContract(t *testing.T) {
	hooks := ComposeHooks([]namedInterceptor{{
		name: "x", points: []string{InterceptTool}, inner: boomAfter{},
	}}, nil)
	res, err := hooks.AfterTool(context.Background(), "Read", nil, loop.ToolResult{Content: []types.Content{{Type: "text", Text: "ok"}}})
	if err != nil || res.Content[0].Text != "ok" {
		t.Fatalf("%v %+v", err, res)
	}
}

type boomAfter struct{ NopInterceptor }

func (boomAfter) AfterTool(context.Context, ToolCall, ResultPatch) (ResultPatch, error) {
	return ResultPatch{}, errRPC
}
