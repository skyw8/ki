package codetools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"ki/internal/codemode"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/tool/builtin/internal/support"
	"ki/internal/tool/discovery"
	"ki/internal/types"
)

const defaultTokens = 10000
const maxSafeInteger = 1<<53 - 1

const execGrammar = `start: pragma_source | plain_source
pragma_source: PRAGMA_LINE NEWLINE SOURCE
plain_source: SOURCE
PRAGMA_LINE: /[ \t]*\/\/ @exec:[^\r\n]*/
NEWLINE: /\r?\n/
SOURCE: /[\s\S]+/`

const execPrompt = `Run JavaScript to orchestrate and compose tool calls.
- Each exec evaluates an async function body in a fresh goja VM. Use await; expression results are not automatically output.
- No Node, filesystem, network, console or module imports. Real operations must use tools.
- Function tools accept an object; freeform tools accept a string. Every tool returns a Promise of {content, details, isError}; inspect isError. Tool argument/policy/transport failures can reject the Promise.
- Use text(value) to emit text and image(dataURLOrContentBlock) to forward an image. generatedImage accepts {image_url, output_hint?}. HTTP URLs and filesystem paths are not images.
- tools contains the enabled nested tools. ALL_TOOLS lists their name and description.
- store(key,value) and load(key) share JSON values within this session. Each exec snapshots state; writes become shared only when the cell completes, including script errors. Termination discards writes.
- exit() ends the script successfully. Await tool calls: normal completion cancels and joins unawaited callbacks.
- setTimeout/clearTimeout are available; pending timers alone do not keep a script alive.
- yield_control() yields accumulated output while the script continues. A running result includes a cell ID; use wait to obtain only new output or terminate it.
- notify(value) immediately reports attributed progress; its text reaches the model at the next exec/wait observation, not through a separate provider-specific output.
- Optional first-line pragma: // @exec: {"yield_time_ms":10000,"max_output_tokens":10000}
- The pragma accepts only those two fields as non-negative safe integers, including zero. Yield time is an observation timeout, not an execution deadline; observations cap at 60000ms.
- Output tokens are approximated as four UTF-8 bytes each and bounded by the runtime output cap. Only explicit output reaches the model. Audio is not supported.`

// Set binds exec/wait to a logical session and its advertised capabilities.
// A nil Session is useful for the settings catalog but cannot execute.
type Set struct {
	Session *codemode.Session
	// GetSession lazily reserves a worker session on the first actual call;
	// publishing schemas must not consume the worker admission budget.
	GetSession func() (*codemode.Session, error)
	Callbacks  codemode.Callbacks
	Nested     []toolapi.Tool
	// Deferred contains canonical names whose declarations are omitted from
	// the exec prompt. Runtime tools and ALL_TOOLS retain these capabilities.
	Deferred map[string]bool
	Freeform bool
}

func (s Set) Build() []toolapi.Tool {
	defs, description := definitionsWithDeferred(s.Nested, s.Deferred)
	return []toolapi.Tool{
		&execTool{session: s.Session, getSession: s.GetSession, callbacks: s.Callbacks, tools: defs, description: description, freeform: s.Freeform},
		&waitTool{session: s.Session, getSession: s.GetSession},
	}
}

func definitions(nested []toolapi.Tool) ([]codemode.ToolDefinition, string) {
	return definitionsWithDeferred(nested, nil)
}

func definitionsWithDeferred(nested []toolapi.Tool, deferred map[string]bool) ([]codemode.ToolDefinition, string) {
	sorted := slices.Clone(nested)
	slices.SortFunc(sorted, func(a, b toolapi.Tool) int {
		return strings.Compare(toolapi.MustCanonical(a.Name()), toolapi.MustCanonical(b.Name()))
	})
	var defs []codemode.ToolDefinition
	var b strings.Builder
	omitted := false
	for _, t := range sorted {
		spec := toolapi.SpecFor(t)
		if spec.Name == catalog.CodeExec || spec.Name == catalog.CodeWait || spec.Name == discovery.Name {
			continue
		}
		description := strings.TrimSpace(spec.Description)
		freeform := spec.Type == "custom"
		if freeform {
			description += "\nInput: raw string."
		} else {
			schema, _ := json.Marshal(spec.Parameters)
			description += "\nInput JSON schema: " + string(schema)
		}
		defs = append(defs, codemode.ToolDefinition{Name: spec.Name, Description: description, Freeform: freeform})
		if deferred[spec.Name] {
			omitted = true
			continue
		}
		fmt.Fprintf(&b, "\n\n### tools.%s\n%s", spec.Name, description)
	}
	if omitted {
		b.WriteString("\n\nSome deferred nested tool declarations are omitted here. They are already available on tools and listed in ALL_TOOLS; filter ALL_TOOLS by name and description to find them. Search discovers schemas, not execution permissions. search_tool is a separate model tool, not a tools method.")
	}
	return defs, b.String()
}

type execTool struct {
	session     *codemode.Session
	getSession  func() (*codemode.Session, error)
	callbacks   codemode.Callbacks
	tools       []codemode.ToolDefinition
	description string
	freeform    bool
}

func (*execTool) Name() string        { return catalog.CodeExec }
func (*execTool) Description() string { return "Execute JavaScript to orchestrate tool calls" }
func (*execTool) Snippet() string     { return "Run JavaScript with tools, await, and explicit output" }
func (t *execTool) Prompt() string    { return execPrompt + t.description }
func (*execTool) Parameters() map[string]any {
	return support.ObjectSchema([]any{"code"}, map[string]any{
		"code": map[string]any{"type": "string", "description": "JavaScript source, optionally preceded by a first-line // @exec: pragma"},
	})
}
func (t *execTool) ToolSpec() toolapi.Spec {
	description := t.Description() + "\n\n" + t.Prompt()
	if t.freeform {
		return toolapi.Spec{Type: "custom", Name: t.Name(), Description: description, Format: &toolapi.Format{Type: "grammar", Syntax: "lark", Definition: execGrammar}}
	}
	return toolapi.Spec{Type: "function", Name: t.Name(), Description: description, Parameters: t.Parameters()}
}
func (t *execTool) Validate(args map[string]any) error {
	if err := support.ValidateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	_, err := parseSource(support.StringArg(args, "code", ""))
	return err
}
func (t *execTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	if err := t.Validate(args); err != nil {
		return inputError(err)
	}
	return t.ExecuteRaw(ctx, args["code"].(string))
}
func (t *execTool) ExecuteRaw(ctx context.Context, input string) toolapi.Result {
	req, err := parseSource(input)
	if err != nil {
		return inputError(err)
	}
	req.ParentCallID = toolapi.ExecutionFromContext(ctx).CallID
	req.Tools = t.tools
	session := t.session
	if t.getSession != nil {
		session, err = t.getSession()
		if err != nil {
			return result(codemode.Response{}, err)
		}
	}
	response, err := session.Execute(ctx, req, t.callbacks)
	return result(response, err)
}

type waitTool struct {
	session    *codemode.Session
	getSession func() (*codemode.Session, error)
}

func (*waitTool) Name() string        { return catalog.CodeWait }
func (*waitTool) Description() string { return "Observe or terminate a running JavaScript cell" }
func (*waitTool) Snippet() string     { return "Get new output from an exec cell or terminate it" }
func (*waitTool) Prompt() string {
	return "Use only after exec returns Script running with cell ID .... Returns only new output. Defaults: yield_time_ms=10000, max_tokens=10000. terminate=true stops the cell. Zero budgets are valid. Observation timeout caps at 60000ms."
}
func (*waitTool) Parameters() map[string]any {
	return support.ObjectSchema([]any{"cell_id"}, map[string]any{
		"cell_id":       map[string]any{"type": "string", "description": "The running exec cell ID"},
		"yield_time_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": maxSafeInteger},
		"max_tokens":    map[string]any{"type": "integer", "minimum": 0, "maximum": maxSafeInteger},
		"terminate":     map[string]any{"type": "boolean"},
	})
}
func (t *waitTool) Validate(args map[string]any) error {
	if err := support.ValidateArgs(t.Parameters(), t.Name(), args); err != nil {
		return err
	}
	_, err := parseWait(args)
	return err
}
func (t *waitTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	if err := t.Validate(args); err != nil {
		return inputError(err)
	}
	req, _ := parseWait(args)
	session := t.session
	if t.getSession != nil {
		var err error
		session, err = t.getSession()
		if err != nil {
			return result(codemode.Response{}, err)
		}
	}
	response, err := session.Wait(ctx, req)
	return result(response, err)
}

func parseSource(source string) (codemode.ExecuteRequest, error) {
	req := codemode.ExecuteRequest{Source: source, MaxOutputBytes: tokenBytes(defaultTokens), MaxOutputBytesSet: true}
	if strings.TrimSpace(source) == "" {
		return req, fmt.Errorf("exec expects non-empty JavaScript source")
	}
	first, rest, _ := strings.Cut(source, "\n")
	pragma, ok := strings.CutPrefix(strings.TrimLeft(first, " \t"), "// @exec:")
	if !ok {
		return req, nil
	}
	if strings.TrimSpace(rest) == "" {
		return req, fmt.Errorf("exec pragma must be followed by JavaScript source")
	}
	decoder := json.NewDecoder(strings.NewReader(pragma))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return req, fmt.Errorf("exec pragma must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return req, fmt.Errorf("exec pragma contains trailing data")
	}
	for key := range fields {
		if key != "yield_time_ms" && key != "max_output_tokens" {
			return req, fmt.Errorf("exec pragma only accepts yield_time_ms and max_output_tokens; got %q", key)
		}
	}
	if value, ok := fields["yield_time_ms"]; ok {
		n, err := safeInteger(value, "yield_time_ms")
		if err != nil {
			return req, err
		}
		req.YieldTime, req.YieldTimeSet = observationTime(n), true
	}
	if value, ok := fields["max_output_tokens"]; ok {
		n, err := safeInteger(value, "max_output_tokens")
		if err != nil {
			return req, err
		}
		req.MaxOutputBytes = tokenBytes(n)
	}
	req.Source = rest
	return req, nil
}

func parseWait(args map[string]any) (codemode.WaitRequest, error) {
	req := codemode.WaitRequest{CellID: support.StringArg(args, "cell_id", ""), MaxOutputBytes: tokenBytes(defaultTokens), MaxOutputBytesSet: true}
	if strings.TrimSpace(req.CellID) == "" {
		return req, fmt.Errorf("wait requires a non-empty cell_id")
	}
	if value := args["yield_time_ms"]; value != nil {
		n, err := safeInteger(value, "yield_time_ms")
		if err != nil {
			return req, err
		}
		req.YieldTime, req.YieldTimeSet = observationTime(n), true
	}
	if value := args["max_tokens"]; value != nil {
		n, err := safeInteger(value, "max_tokens")
		if err != nil {
			return req, err
		}
		req.MaxOutputBytes = tokenBytes(n)
	}
	req.Terminate, _ = args["terminate"].(bool)
	return req, nil
}

func safeInteger(value any, field string) (int, error) {
	n, ok := support.AsInt(value)
	if !ok || n < 0 || n > maxSafeInteger {
		return 0, fmt.Errorf("%s must be a non-negative JavaScript-safe integer", field)
	}
	return n, nil
}

func observationTime(ms int) time.Duration {
	// Clamp before multiplication: safe JS integers can overflow Go durations.
	return time.Duration(min(ms, 60000)) * time.Millisecond
}
func tokenBytes(tokens int) int {
	return min(tokens, codemode.DefaultLimits().MaxOutputBytes/4) * 4
}
func inputError(err error) toolapi.Result {
	return support.DiagnosticError(err.Error(), "rejected", "invalid_arguments", "model_input")
}
func result(response codemode.Response, err error) toolapi.Result {
	if err != nil {
		return support.DiagnosticError("Code Mode: "+err.Error(), "failed", "runtime_failed", "harness")
	}
	header := "Script completed"
	switch response.Status {
	case "running":
		header = "Script running with cell ID " + response.CellID
	case "terminated":
		header = "Script terminated"
	case "failed":
		header = "Script failed"
	}
	content := append([]types.Content{{Type: "text", Text: header}}, response.Content...)
	if response.Error != "" {
		content = append(content, types.Content{Type: "text", Text: "Script error:\n" + response.Error})
	}
	return toolapi.Result{Content: content, IsError: response.Status == "failed", Terminate: response.Terminate,
		Details: map[string]any{"cell_id": response.CellID, "status": response.Status, "truncated": response.Truncated}}
}
