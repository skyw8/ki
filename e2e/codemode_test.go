package e2e

import (
	"context"
	"fmt"
	"testing"

	"ki/internal/codemode"
	"ki/internal/types"
)

func TestCodeModeWorkerRealBinary(t *testing.T) {
	t.Parallel()
	// Unit workers dispatch through a test-only entry point. Use the shared
	// real CLI build to verify default worker argv, secret-free environment,
	// bootstrap limits, and stdout framing through the production entry point.
	manager := codemode.NewManager(codemode.Config{Executable: builtKI(t)})
	// t.Context is canceled before cleanup; keep the worker lifetime alive
	// until Manager.Close has completed its graceful shutdown IPC.
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	t.Cleanup(func() {
		defer cancel()
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	session, err := manager.NewSession(ctx, "real-worker-smoke")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "KI-WORKER-MARKER-64"
	calls := make(chan codemode.Invocation, 1)
	response, err := session.Execute(t.Context(), codemode.ExecuteRequest{
		ParentCallID: "outer-1",
		Source:       "const r = await tools.echo({marker: '" + marker + "'}); store('marker', r.content[0].text); text(r.content[0].text);",
		Tools:        []codemode.ToolDefinition{{Name: "echo", Description: "Return the fixture marker"}},
	}, codemode.Callbacks{Invoke: func(_ context.Context, invocation codemode.Invocation) (codemode.ToolResult, error) {
		if invocation.Name != "echo" || invocation.Arguments["marker"] != marker ||
			invocation.ParentCallID != "outer-1" || invocation.CellID == "" || invocation.ToolCallID == "" {
			return codemode.ToolResult{}, fmt.Errorf("unexpected worker callback: %+v", invocation)
		}
		calls <- invocation
		return codemode.ToolResult{Content: []types.Content{{Type: "text", Text: marker}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "completed" || response.Error != "" || len(response.Content) != 1 || response.Content[0].Text != marker {
		t.Fatalf("worker response: %+v", response)
	}
	select {
	case <-calls:
	default:
		t.Fatal("real worker did not invoke the parent callback")
	}
	// Reuse the worker to check that no startup noise/relaunch corrupts the
	// next IPC exchange and completed cells commit their session store.
	response, err = session.Execute(t.Context(), codemode.ExecuteRequest{
		ParentCallID: "outer-2", Source: "text(load('marker'));",
	}, codemode.Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "completed" || response.Error != "" || len(response.Content) != 1 || response.Content[0].Text != marker {
		t.Fatalf("worker continuation: %+v", response)
	}
}
