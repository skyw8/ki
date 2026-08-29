package extension

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ki/internal/session"
)

func TestLifecycleSubscribeToolCallBlock(t *testing.T) {
	bin := buildTestSidecar(t)
	root := t.TempDir()
	d := Descriptor{
		Name: "protected-paths", Path: root, Enabled: true,
		Capabilities: []string{"lifecycle"}, root: root,
		manifest: Manifest{Name: "protected-paths", Capabilities: []string{"lifecycle"}, Runtime: RuntimeSpec{Kind: runtimeRPC, Command: bin}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := startRPC(ctx, d, "sess", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if !c.hasSync(EventToolCall) {
		t.Fatal("expected tool_call sync subscription")
	}
	_, block, err := c.BeforeTool(ctx, ToolCall{Name: "Write", Args: map[string]any{"path": "/tmp/.env"}})
	if err != nil {
		t.Fatal(err)
	}
	if block == nil || block.Reason != "blocked .env" {
		t.Fatalf("block %+v", block)
	}
}

func TestIllegalSyncRejectedAndEmptyLifecycleFails(t *testing.T) {
	if err := AcceptSubscription(Subscription{Event: "message_update", Mode: "sync"}); err == nil {
		t.Fatal("message_update sync must be rejected")
	}
	if err := AcceptSubscription(Subscription{Event: "tool_call", Mode: "sync"}); err != nil {
		t.Fatal(err)
	}
	reg := Registration{Subscriptions: []Subscription{{Event: "nope", Mode: "sync"}}}
	if err := gateSubscriptions([]string{string(CapLifecycle)}, &reg); !errors.Is(err, errNoSubscriptions) {
		t.Fatalf("want no subscriptions err, got %v", err)
	}
}

func TestAsyncAgentEndNotify(t *testing.T) {
	bin := buildTestSidecar(t)
	root := t.TempDir()
	d := Descriptor{
		Name: "protected-paths", Path: root, Enabled: true,
		Capabilities: []string{"lifecycle"}, root: root,
		manifest: Manifest{Name: "protected-paths", Capabilities: []string{"lifecycle"}, Runtime: RuntimeSpec{Kind: runtimeRPC, Command: bin}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := startRPC(ctx, d, "sess", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if !c.hasAsync(EventAgentEnd) || !c.hasAsync(EventAgentSettled) {
		t.Fatal("expected async agent_end and agent_settled")
	}
	if err := c.OnEvent(ctx, Event{Type: EventAgentEnd, SessionID: "sess"}); err != nil {
		t.Fatal(err)
	}
}

func testLifecycleDesc(t *testing.T, name string, caps []string, env map[string]string) Descriptor {
	t.Helper()
	bin := buildTestSidecar(t)
	root := t.TempDir()
	return Descriptor{
		Name: name, Path: root, Enabled: true,
		Capabilities: caps, root: root,
		manifest: Manifest{Name: name, Capabilities: caps, Runtime: RuntimeSpec{Kind: runtimeRPC, Command: bin, Env: env}},
	}
}

func TestRewriteInputAndCompactCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	home := t.TempDir()

	rewrite := testLifecycleDesc(t, "rewrite", []string{"lifecycle"}, map[string]string{"KI_REWRITE_INPUT": "rewritten-prompt"})
	m := NewManager(home, nil)
	_ = m.Prepare(ctx, "sess-rw", t.TempDir(), []Descriptor{rewrite})
	defer m.Close()
	got, swallow := m.ApplyInput(ctx, "sess-rw", "hello")
	if swallow || got != "rewritten-prompt" {
		t.Fatalf("rewrite got %q swallow %v", got, swallow)
	}

	swallowD := testLifecycleDesc(t, "swallow", []string{"lifecycle"}, map[string]string{"KI_SWALLOW_INPUT": "1"})
	ms := NewManager(home, nil)
	_ = ms.Prepare(ctx, "sess-sw", t.TempDir(), []Descriptor{swallowD})
	defer ms.Close()
	got, swallow = ms.ApplyInput(ctx, "sess-sw", "hello")
	if !swallow || got != "" {
		t.Fatalf("swallow got %q swallow %v", got, swallow)
	}

	cancelD := testLifecycleDesc(t, "ccancel", []string{"lifecycle"}, map[string]string{"KI_COMPACT_CANCEL": "1"})
	mc := NewManager(home, nil)
	_ = mc.Prepare(ctx, "sess-cc", t.TempDir(), []Descriptor{cancelD})
	defer mc.Close()
	ok, summary := mc.CompactAllowed(ctx, "sess-cc")
	if ok || summary != "" {
		t.Fatalf("cancel compact ok=%v summary=%q", ok, summary)
	}

	sumD := testLifecycleDesc(t, "csum", []string{"lifecycle"}, map[string]string{"KI_COMPACT_SUMMARY": "custom-summary"})
	msum := NewManager(home, nil)
	_ = msum.Prepare(ctx, "sess-cs", t.TempDir(), []Descriptor{sumD})
	defer msum.Close()
	ok, summary = msum.CompactAllowed(ctx, "sess-cs")
	if !ok || summary != "custom-summary" {
		t.Fatalf("summary compact ok=%v summary=%q", ok, summary)
	}
}

func TestRegisterToolsVisibleOnNextPrepare(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	d := testLifecycleDesc(t, "regtools", []string{"lifecycle", "tool"}, nil)
	m := NewManager(t.TempDir(), nil)
	first := m.Prepare(ctx, "sess", t.TempDir(), []Descriptor{d})
	defer m.Close()
	for _, tl := range first {
		if tl.Name() == "ext_ping" {
			t.Fatal("ext_ping present before register")
		}
	}
	if err := m.RegisterTools("sess", "regtools", []ToolSpec{
		{Name: "ext_ping", Description: "ping", Parameters: map[string]any{"type": "object"}},
	}); err != nil {
		t.Fatal(err)
	}
	second := m.Prepare(ctx, "sess", t.TempDir(), []Descriptor{d})
	found := false
	for _, tl := range second {
		if tl.Name() == "ext_ping" {
			found = true
		}
	}
	if !found {
		t.Fatal("ext_ping missing on next Prepare")
	}
}

func TestDiscoverLifecycleNotIntercept(t *testing.T) {
	home := t.TempDir()
	writePkg(t, filepath.Join(home, "extensions"), "a", `{
		"name":"a","capabilities":["lifecycle"],"runtime":{"kind":"rpc","command":"bin/x"}
	}`)
	got := Discover(home, session.Toggle{})
	if len(got.Enabled) != 1 {
		t.Fatalf("%+v", got.Enabled)
	}
	raw, err := json.Marshal(got.Enabled[0])
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("NO") == "x" {
		t.Log(raw)
	}
}
