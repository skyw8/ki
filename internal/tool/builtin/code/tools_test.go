package codetools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ki/internal/codemode"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/types"
)

func TestExecPragma(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		wantErr      bool
	}{
		{"plain", "text(1)", false},
		{"empty", " \n\t", true},
		{"defaults", "// @exec: {}\ntext(1)", false},
		{"zero", "// @exec: {\"yield_time_ms\":0,\"max_output_tokens\":0}\ntext(1)", false},
		{"safe maximum", "// @exec: {\"yield_time_ms\":9007199254740991,\"max_output_tokens\":9007199254740991}\ntext(1)", false},
		{"unsafe", "// @exec: {\"yield_time_ms\":9007199254740992}\ntext(1)", true},
		{"fraction", "// @exec: {\"yield_time_ms\":0.5}\ntext(1)", true},
		{"negative", "// @exec: {\"max_output_tokens\":-1}\ntext(1)", true},
		{"null", "// @exec: {\"yield_time_ms\":null}\ntext(1)", true},
		{"unknown", "// @exec: {\"timeout\":1}\ntext(1)", true},
		{"array", "// @exec: []\ntext(1)", true},
		{"trailing", "// @exec: {} {}\ntext(1)", true},
		{"no source", "// @exec: {}\n \t", true},
		{"CRLF", " \t// @exec: {\"yield_time_ms\":42}\r\ntext(1)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := parseSource(tc.source)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parse = %+v, %v", req, err)
			}
			if err == nil && strings.HasPrefix(strings.TrimSpace(tc.source), "// @exec:") && req.Source != "text(1)" {
				t.Fatalf("source = %q", req.Source)
			}
			if tc.name == "zero" && (req.YieldTime != 0 || !req.YieldTimeSet || req.MaxOutputBytes != 0 || !req.MaxOutputBytesSet) {
				t.Fatalf("lost explicit zero: %+v", req)
			}
			if tc.name == "safe maximum" && (req.YieldTime != time.Minute || req.MaxOutputBytes != codemode.DefaultLimits().MaxOutputBytes) {
				t.Fatalf("limits overflowed: %+v", req)
			}
		})
	}
}

func TestWaitLimits(t *testing.T) {
	for _, value := range []any{-1, 0.5, float64(1 << 53), "10", json.Number("9223372036854775808")} {
		if _, err := parseWait(map[string]any{"cell_id": "c", "yield_time_ms": value}); err == nil {
			t.Fatalf("accepted invalid yield %v", value)
		}
	}
	req, err := parseWait(map[string]any{"cell_id": "c", "yield_time_ms": 0, "max_tokens": 0, "terminate": true})
	if err != nil || !req.YieldTimeSet || !req.MaxOutputBytesSet || req.MaxOutputBytes != 0 || !req.Terminate {
		t.Fatalf("wait = %+v, %v", req, err)
	}
	wait := &waitTool{}
	if err := wait.Validate(map[string]any{"cell_id": "c", "terminate": "yes"}); err == nil {
		t.Fatal("invalid boolean")
	}
	if err := wait.Validate(map[string]any{"cell_id": " "}); err == nil {
		t.Fatal("empty ID")
	}
}

func TestExecProviderShapes(t *testing.T) {
	for _, freeform := range []bool{false, true} {
		tools := (Set{Freeform: freeform}).Build()
		spec := toolapi.SpecFor(tools[0])
		if spec.Name != catalog.CodeExec {
			t.Fatalf("name = %q", spec.Name)
		}
		if freeform {
			if spec.Type != "custom" || spec.Format == nil || spec.Format.Syntax != "lark" {
				t.Fatalf("custom spec = %+v", spec)
			}
		} else if spec.Type != "function" || spec.Parameters == nil {
			t.Fatalf("function spec = %+v", spec)
		}
		if err := tools[0].(toolapi.Validator).Validate(map[string]any{"code": "text(1)", "unknown": 1}); err == nil {
			t.Fatal("extra property")
		}
	}
}

func TestExecWaitAdapter(t *testing.T) {
	session := codemode.NewLocalSession(codemode.Limits{})
	defer session.Close()
	ctx := toolapi.WithExecutionIdentity(t.Context(), toolapi.ExecutionIdentity{CallID: "outer"})
	tools := (Set{Session: session}).Build()
	result := tools[0].Execute(ctx, map[string]any{"code": "store('answer',42); text(load('answer')); text(typeof console)"})
	if result.IsError || !strings.Contains(textOf(result), "42") || !strings.Contains(textOf(result), "undefined") {
		t.Fatalf("result = %+v", result)
	}
	zero := tools[0].Execute(ctx, map[string]any{"code": "// @exec: {\"max_output_tokens\":0}\ntext('hidden')"})
	if zero.IsError || strings.Contains(textOf(zero), "hidden") || !strings.Contains(textOf(zero), "Script completed") {
		t.Fatalf("zero result = %+v", zero)
	}
	failure := tools[0].Execute(ctx, map[string]any{"code": "throw Error('boom')"})
	if !failure.IsError || !strings.Contains(textOf(failure), "Script failed") || !strings.Contains(textOf(failure), "boom") {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestDefinitionsKeepFreeformAndExcludeRecursion(t *testing.T) {
	tools := (Set{}).Build()
	nested := append(tools, fakeTool{name: "apply_patch", custom: true})
	defs, prompt := definitions(nested)
	if len(defs) != 1 || defs[0].Name != "apply_patch" || !defs[0].Freeform || !strings.Contains(prompt, "raw string") {
		t.Fatalf("definitions = %+v\n%s", defs, prompt)
	}
}

func TestDeferredDescriptionsDoNotRevokeRuntimeCapabilities(t *testing.T) {
	session := codemode.NewLocalSession(codemode.Limits{})
	defer session.Close()
	called := false
	nested := []toolapi.Tool{fakeTool{name: "hidden_document"}, fakeTool{name: "visible"}, fakeTool{name: "search_tool"}}
	set := Set{Session: session, Nested: nested, Deferred: map[string]bool{"hidden_document": true}, Callbacks: codemode.Callbacks{
		Invoke: func(_ context.Context, call codemode.Invocation) (codemode.ToolResult, error) {
			called = call.Name == "hidden_document"
			return codemode.ToolResult{Content: []types.Content{{Type: "text", Text: "known deferred result"}}}, nil
		},
	}}
	tools := set.Build()
	prompt := tools[0].Prompt()
	if strings.Contains(prompt, "### tools.hidden_document") || strings.Contains(prompt, "### tools.search_tool") ||
		!strings.Contains(prompt, "### tools.visible") || !strings.Contains(prompt, "filter ALL_TOOLS") {
		t.Fatalf("deferred prompt %s", prompt)
	}
	ctx := toolapi.WithExecutionIdentity(t.Context(), toolapi.ExecutionIdentity{CallID: "outer"})
	result := tools[0].Execute(ctx, map[string]any{"code": "text(ALL_TOOLS.map(t => t.name)); text(typeof tools.search_tool); text(await tools.hidden_document({}));"})
	if result.IsError || !called || !strings.Contains(textOf(result), "hidden_document") ||
		!strings.Contains(textOf(result), "undefined") || !strings.Contains(textOf(result), "known deferred result") {
		t.Fatalf("deferred runtime capability lost: %+v", result)
	}
	set.Deferred = nil
	if !strings.Contains(set.Build()[0].Prompt(), "### tools.hidden_document") {
		t.Fatal("loaded declaration could not be restored")
	}
}

func TestSchemasDoNotReserveWorkerSession(t *testing.T) {
	calls := 0
	tools := (Set{GetSession: func() (*codemode.Session, error) {
		calls++
		return nil, errors.New("test admission")
	}}).Build()
	for _, tool := range tools {
		_ = toolapi.SpecFor(tool)
		_ = tool.Prompt()
	}
	if calls != 0 {
		t.Fatal("schema publication reserved a worker")
	}
	ctx := toolapi.WithExecutionIdentity(t.Context(), toolapi.ExecutionIdentity{CallID: "test"})
	invalid := tools[0].Execute(ctx, map[string]any{"code": " "})
	if !invalid.IsError || calls != 0 {
		t.Fatal("invalid code reserved a worker")
	}
	valid := tools[0].Execute(ctx, map[string]any{"code": "text(1)"})
	if !valid.IsError || calls != 1 || !strings.Contains(textOf(valid), "test admission") {
		t.Fatalf("execution failed to reserve lazily: %+v, calls=%d", valid, calls)
	}
}

type fakeTool struct {
	name   string
	custom bool
}

func (t fakeTool) Name() string                                         { return t.name }
func (fakeTool) Description() string                                    { return "test" }
func (fakeTool) Prompt() string                                         { return "" }
func (fakeTool) Snippet() string                                        { return "" }
func (fakeTool) Parameters() map[string]any                             { return map[string]any{"type": "object"} }
func (fakeTool) Execute(context.Context, map[string]any) toolapi.Result { return toolapi.Result{} }
func (t fakeTool) ToolSpec() toolapi.Spec {
	typ := "function"
	if t.custom {
		typ = "custom"
	}
	return toolapi.Spec{Name: t.name, Type: typ, Description: "test", Parameters: t.Parameters()}
}
func textOf(result toolapi.Result) string { return (types.Message{Content: result.Content}).Text() }
