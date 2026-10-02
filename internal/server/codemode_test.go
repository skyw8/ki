package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ki/internal/codemode"
	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/session"
	"ki/internal/toggles"
	toolapi "ki/internal/tool"
	"ki/internal/types"
)

// The worker reuses this test executable rather than building another binary.
// Exit directly: testing's PASS banner would corrupt the NDJSON transport.
func TestCodeModeServerWorkerProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != codemode.WorkerArg {
		return
	}
	if err := codemode.ServeWorker(context.Background(), os.Stdin, os.Stdout, codemode.DefaultLimits()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type codeModeServerStreamer func(context.Context, loop.Request) (types.Message, error)

func (f codeModeServerStreamer) Stream(ctx context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	return f(ctx, req)
}

func codeModeTestServer(t *testing.T, streamer loop.Streamer) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	cfg := config.Builtin(home)
	cfg.Compaction.Enabled = false
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Config: cfg, Token: "tok", Streamer: streamer,
		CodeModeConfig: &codemode.Config{
			Executable: executable,
			Args:       []string{"-test.run=^TestCodeModeServerWorkerProcess$", "--", codemode.WorkerArg},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return srv
}

func codeModeTestSession(t *testing.T, srv *Server, cwd, api string) string {
	t.Helper()
	var ref provider.ModelRef
	for _, p := range srv.registry.Providers() {
		for _, m := range p.Models {
			if p.Enabled && m.Enabled && m.API == api {
				ref = provider.ModelRef{Provider: p.ID, Model: m.ID}
				break
			}
		}
		if ref.Model != "" {
			break
		}
	}
	if ref.Model == "" {
		t.Fatalf("no catalog model with API %q", api)
	}
	sess, err := session.Create(srv.cfg.Sessions.Root, cwd, ref.Provider, ref.Model)
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	srv.sidx.Add(id, sess.Dir)
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	return id
}

func codeModeRunPrompt(t *testing.T, srv *Server, id, text string) *runState {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	st, runCtx, err := srv.occupy(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	srv.runPrompt(runCtx, st, id, []types.Content{{Type: "text", Text: text}}, nil, "", "", "", nil)
	if st.err != nil {
		t.Fatalf("run %q: %v", text, st.err)
	}
	return st
}

func codeModeEntries(t *testing.T, srv *Server, id string) []session.Entry {
	t.Helper()
	sess, err := srv.open(id)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	return sess.Entries()
}

func codeModeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func codeModeStop() types.Message {
	return types.Message{Role: "assistant", StopReason: "stop", Content: []types.Content{{Type: "text", Text: "done"}}}
}

func codeModeExecCall(req loop.Request, id, source string) types.Message {
	call := types.Content{Type: "toolCall", ID: id, Name: "exec", Arguments: map[string]any{"code": source}}
	for _, spec := range req.Tools {
		if spec.Name == "exec" && spec.Type == "custom" {
			call.ToolType, call.Input, call.Arguments = "custom", source, nil
		}
	}
	return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{call}}
}

func codeModeTurn(req loop.Request) (string, []types.Message) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			var results []types.Message
			for _, msg := range req.Messages[i+1:] {
				if msg.Role == "toolResult" {
					results = append(results, msg)
				}
			}
			return req.Messages[i].Text(), results
		}
	}
	return "", nil
}

func TestCodeModeServerSchemaExposure(t *testing.T) {
	for _, api := range []string{"completions", "responses", "anthropic"} {
		t.Run(api, func(t *testing.T) {
			var got loop.Request
			srv := codeModeTestServer(t, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
				got = req
				return codeModeStop(), nil
			}))
			cwd := t.TempDir()
			skill := filepath.Join(cwd, ".ki", "skills", "code-fixture", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(skill, []byte("---\nname: code-fixture\ndescription: code mode skill fixture\n---\nUse this fixture.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			id := codeModeTestSession(t, srv, cwd, api)
			codeModeRunPrompt(t, srv, id, "schema")
			names := make([]string, 0, len(got.Tools))
			var exec toolapi.Spec
			for _, spec := range got.Tools {
				names = append(names, spec.Name)
				if spec.Name == "exec" {
					exec = spec
				}
			}
			if !slices.Contains(names, "exec") || !slices.Contains(names, "wait") {
				t.Fatalf("missing code mode schemas: %v", names)
			}
			if !slices.Contains(names, "read") || !strings.Contains(got.System, "code-fixture") {
				t.Fatalf("direct read or skill disappeared: %v / %s", names, got.System)
			}
			if !strings.Contains(exec.Description, "tools.read") || !strings.Contains(exec.Description, "file_path") {
				t.Fatal("exec description omitted nested tool signatures")
			}
			if api == "responses" {
				if exec.Type != "custom" || exec.Format == nil || exec.Format.Syntax != "lark" {
					t.Fatalf("Responses exec is not native freeform: %+v", exec)
				}
			} else if exec.Type != "function" || exec.Parameters == nil {
				t.Fatalf("JSON code fallback missing: %+v", exec)
			}
		})
	}
}

func TestCodeModeServerDisabledToolsCannotReappearThroughCode(t *testing.T) {
	var result types.Message
	srv := codeModeTestServer(t, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		_, results := codeModeTurn(req)
		if len(results) == 0 {
			for _, spec := range req.Tools {
				if spec.Name == "read" {
					return types.Message{}, errors.New("disabled read was advertised directly")
				}
				if spec.Name == "exec" && strings.Contains(spec.Description, "### tools.read\n") {
					return types.Message{}, errors.New("disabled read was advertised in exec")
				}
			}
			return codeModeExecCall(req, "disabled-read", `text(JSON.stringify({listed:ALL_TOOLS.some(t=>t.name==="read"),available:typeof tools.read}));
try { await tools.read({file_path:"disabled.txt"}); text("escaped"); } catch (e) { text("blocked"); }`), nil
		}
		if len(results) != 1 {
			return types.Message{}, fmt.Errorf("unexpected results: %d", len(results))
		}
		result = results[0]
		return codeModeStop(), nil
	}))
	if err := toggles.Save(srv.cfg.Home, toggles.File{Tools: session.Toggle{Disabled: []string{"read"}}}); err != nil {
		t.Fatal(err)
	}
	id := codeModeTestSession(t, srv, t.TempDir(), "completions")
	codeModeRunPrompt(t, srv, id, "disabled")
	if result.IsError || !strings.Contains(result.Text(), `{"listed":false,"available":"undefined"}`) || !strings.Contains(result.Text(), "blocked") || strings.Contains(result.Text(), "escaped") {
		t.Fatalf("disabled capability escaped: %+v", result)
	}
	for _, entry := range codeModeEntries(t, srv, id) {
		if entry.Type == string(loop.ToolExecutionStart) && strings.Contains(codeModeJSON(t, entry.Details), `"toolName":"read"`) {
			t.Fatalf("disabled tool reached dispatcher: %+v", entry)
		}
	}
}

func TestCodeModeServerReadFiltersFullIntermediateAndPersistsNestedAudit(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "large.txt")
	// The marker is past both the 16KiB and 800-line preview boundaries, but
	// inside read's own 2000-line/50KB page. JS must not filter the audit copy.
	body := strings.Repeat("skip:01234567890123456789\n", 1100) + "MATCH:full-intermediate\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`const r = await tools.read({file_path:%s});
if (r.isError) throw new Error(JSON.stringify(r));
const lines = r.content.filter(c => c.type === "text").map(c => c.text).join("\n").split("\n");
text(lines.filter(line => line.startsWith("MATCH:")).join("\n"));
notify("read-notification");`, codeModeJSON(t, path))
	var next loop.Request
	srv := codeModeTestServer(t, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		_, results := codeModeTurn(req)
		if len(results) == 0 {
			return codeModeExecCall(req, "outer-read", source), nil
		}
		next = req
		return codeModeStop(), nil
	}))
	id := codeModeTestSession(t, srv, cwd, "completions")
	st := codeModeRunPrompt(t, srv, id, "read")
	_, results := codeModeTurn(next)
	if len(results) != 1 || results[0].ToolName != "exec" || results[0].ToolCallID != "outer-read" || results[0].IsError {
		t.Fatalf("outer-only provider results: %+v", results)
	}
	if text := results[0].Text(); !strings.Contains(text, "MATCH:full-intermediate") || !strings.Contains(text, "read-notification") || strings.Contains(text, "skip:") {
		t.Fatalf("filtered result or notification missing: %q", text)
	}
	var details struct {
		CellID string `json:"cell_id"`
	}
	if err := json.Unmarshal([]byte(codeModeJSON(t, results[0].Details)), &details); err != nil || details.CellID == "" {
		t.Fatalf("outer cell identity: %+v, %v", results[0].Details, err)
	}
	typesSeen := map[string]int{}
	var nestedID string
	for _, entry := range codeModeEntries(t, srv, id) {
		if entry.Message != nil && entry.Message.Role == "toolResult" && entry.Message.ToolName != "exec" {
			t.Fatalf("nested result leaked into transcript: %+v", entry.Message)
		}
		if entry.Type != string(loop.ToolExecutionStart) && entry.Type != string(loop.ToolExecutionEnd) && entry.Type != string(loop.ToolExecutionUpdate) {
			continue
		}
		var audit map[string]any
		if err := json.Unmarshal([]byte(codeModeJSON(t, entry.Details)), &audit); err != nil {
			t.Fatal(err)
		}
		if audit["parentCallId"] != "outer-read" || audit["cellId"] != details.CellID {
			t.Fatalf("durable nested attribution missing: %+v", audit)
		}
		if audit["toolName"] == "read" {
			callID, _ := audit["toolCallId"].(string)
			if !strings.HasPrefix(callID, "outer-read:nested-") || audit["requestedToolName"] != "read" {
				t.Fatalf("host nested identity: %+v", audit)
			}
			if nestedID != "" && nestedID != callID {
				t.Fatalf("nested start/end identities differ: %q / %q", nestedID, callID)
			}
			nestedID = callID
			typesSeen[entry.Type]++
			if entry.Type == string(loop.ToolExecutionEnd) {
				raw := codeModeJSON(t, audit["result"])
				if strings.Contains(raw, "MATCH:full-intermediate") || !strings.Contains(raw, "truncated") {
					t.Fatalf("audit result is not a separately bounded preview: %s", raw)
				}
			}
		} else if audit["toolName"] == "exec" && entry.Type == string(loop.ToolExecutionUpdate) {
			if !strings.Contains(codeModeJSON(t, audit["partialResult"]), "read-notification") {
				t.Fatalf("notification audit missing text: %+v", audit)
			}
			typesSeen[entry.Type]++
		}
	}
	if typesSeen[string(loop.ToolExecutionStart)] != 1 || typesSeen[string(loop.ToolExecutionEnd)] != 1 || typesSeen[string(loop.ToolExecutionUpdate)] != 1 {
		t.Fatalf("durable start/end/notify audit: %v", typesSeen)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	var starts, ends int
	for _, ev := range st.evs {
		if ev.ParentCallID == "" || ev.ToolName != "read" {
			continue
		}
		if ev.RunID != st.runID || ev.ParentCallID != "outer-read" || ev.CellID != details.CellID || ev.EntryID == "" {
			t.Fatalf("SSE nested event missing durable/run identity: %+v", ev)
		}
		if ev.Type == loop.ToolExecutionStart {
			starts++
		}
		if ev.Type == loop.ToolExecutionEnd {
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("SSE nested start/end = %d/%d", starts, ends)
	}
}

func TestCodeModeServerResponsesExecCallsFreeformPatch(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "patched.txt")
	patch := "*** Begin Patch\n*** Add File: " + filepath.ToSlash(path) + "\n+patched-through-code\n*** End Patch\n"
	source := fmt.Sprintf(`const r = await tools.apply_patch(%s);
if (r.isError) throw new Error(JSON.stringify(r));
text("patch-completed");`, codeModeJSON(t, patch))
	var outer types.Message
	srv := codeModeTestServer(t, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		_, results := codeModeTurn(req)
		if len(results) == 0 {
			return codeModeExecCall(req, "outer-patch", source), nil
		}
		if len(results) != 1 {
			return types.Message{}, fmt.Errorf("unexpected provider result count: %d", len(results))
		}
		outer = results[0]
		return codeModeStop(), nil
	}))
	id := codeModeTestSession(t, srv, cwd, "responses")
	codeModeRunPrompt(t, srv, id, "patch")
	if outer.IsError || outer.ToolType != "custom" || !strings.Contains(outer.Text(), "patch-completed") {
		t.Fatalf("freeform outer result: %+v", outer)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "patched-through-code\n" {
		t.Fatalf("patch content=%q err=%v", content, err)
	}
	var audits int
	for _, entry := range codeModeEntries(t, srv, id) {
		if entry.Message != nil && entry.Message.Role == "toolResult" && entry.Message.ToolName != "exec" {
			t.Fatalf("freeform child became a provider message: %+v", entry.Message)
		}
		if entry.Type == string(loop.ToolExecutionEnd) && strings.Contains(codeModeJSON(t, entry.Details), `"toolName":"apply_patch"`) {
			audits++
		}
	}
	if audits != 1 {
		t.Fatalf("freeform patch audits=%d", audits)
	}
}

func TestCodeModeServerCommittedStoreSurvivesOccupiesAndCancellationDrainsCells(t *testing.T) {
	ready := make(chan types.Message, 1)
	var recovery []types.Message
	var canceledCell string
	srv := codeModeTestServer(t, codeModeServerStreamer(func(ctx context.Context, req loop.Request) (types.Message, error) {
		prompt, results := codeModeTurn(req)
		switch prompt {
		case "commit":
			if len(results) == 0 {
				return codeModeExecCall(req, "outer-commit", `store("kept","committed");text("stored");`), nil
			}
		case "cancel":
			if len(results) == 0 {
				// A mailbox observation provides a real pending parent callback.
				// Cancellation, not elapsed time, must drain it and close the cell.
				return codeModeExecCall(req, "outer-cancel", `store("discarded","uncommitted");const pending=tools.wait_agent({timeout_ms:60000});text("pending");yield_control();await pending;notify("late-notification");`), nil
			}
			ready <- results[0]
			<-ctx.Done()
			return types.Message{}, ctx.Err()
		case "recover":
			if len(results) == 0 {
				exec := codeModeExecCall(req, "outer-recover", `text(JSON.stringify({kept:load("kept"),discarded:load("discarded")??null}));`)
				exec.Content = append(exec.Content, types.Content{
					Type: "toolCall", Name: "wait", ID: "wait-canceled",
					Arguments: map[string]any{"cell_id": canceledCell, "yield_time_ms": 0},
				})
				return exec, nil
			}
			recovery = results
		}
		return codeModeStop(), nil
	}))
	id := codeModeTestSession(t, srv, t.TempDir(), "completions")
	codeModeRunPrompt(t, srv, id, "commit")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	st, runCtx, err := srv.occupy(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runPrompt(runCtx, st, id, []types.Content{{Type: "text", Text: "cancel"}}, nil, "", "", "", nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case msg := <-ready:
		var details struct {
			CellID string `json:"cell_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(codeModeJSON(t, msg.Details)), &details); err != nil || details.CellID == "" || details.Status != "running" || msg.IsError {
			t.Fatalf("pending exec: %+v err=%v", msg, err)
		}
		canceledCell = details.CellID
	case <-ctx.Done():
		t.Fatalf("cell did not yield: %v", ctx.Err())
	}
	// Wait for actual parent-side dispatch, rather than assuming the returned
	// yield means that every worker callback has already crossed the IPC link.
	wake := context.AfterFunc(ctx, func() {
		st.mu.Lock()
		st.wait.Broadcast()
		st.mu.Unlock()
	})
	st.mu.Lock()
	started := false
	for !started && ctx.Err() == nil {
		for _, ev := range st.evs {
			if ev.Type == loop.ToolExecutionStart && ev.ParentCallID == "outer-cancel" && ev.ToolName == "wait_agent" {
				started = true
				break
			}
		}
		if !started {
			st.wait.Wait()
		}
	}
	st.mu.Unlock()
	wake()
	if !started {
		t.Fatalf("parent callback never started: %v", ctx.Err())
	}
	cancel()
	<-done
	if !errors.Is(st.err, context.Canceled) {
		t.Fatalf("canceled occupy error: %v", st.err)
	}
	canceledEntries := codeModeEntries(t, srv, id)
	var callbackStarts, callbackEnds int
	for _, entry := range canceledEntries {
		if entry.Type != string(loop.ToolExecutionStart) && entry.Type != string(loop.ToolExecutionEnd) {
			continue
		}
		var audit map[string]any
		if err := json.Unmarshal([]byte(codeModeJSON(t, entry.Details)), &audit); err != nil {
			t.Fatal(err)
		}
		if audit["parentCallId"] != "outer-cancel" || audit["toolName"] != "wait_agent" {
			continue
		}
		if entry.Type == string(loop.ToolExecutionStart) {
			callbackStarts++
		} else {
			callbackEnds++
			if audit["isError"] != true {
				t.Fatalf("callback did not finish through cancellation: %+v", audit)
			}
		}
	}
	if callbackStarts != 1 || callbackEnds != 1 {
		t.Fatalf("pending callback not drained before release: %d/%d", callbackStarts, callbackEnds)
	}
	before := len(canceledEntries)
	codeModeRunPrompt(t, srv, id, "recover")
	if len(recovery) != 2 {
		t.Fatalf("recovery results=%+v", recovery)
	}
	for _, result := range recovery {
		switch result.ToolName {
		case "exec":
			if result.IsError || !strings.Contains(result.Text(), `{"kept":"committed","discarded":null}`) {
				t.Fatalf("committed state lost or canceled state leaked: %+v", result)
			}
		case "wait":
			if !result.IsError || !strings.Contains(result.Text(), "closed") {
				t.Fatalf("old cell survived occupy cancellation: %+v", result)
			}
		default:
			t.Fatalf("unexpected recovery result: %+v", result)
		}
	}
	for _, entry := range codeModeEntries(t, srv, id)[before:] {
		raw := codeModeJSON(t, entry)
		if strings.Contains(raw, `"parentCallId":"outer-cancel"`) || strings.Contains(raw, "late-notification") {
			t.Fatalf("callback crossed occupy boundary: %s", raw)
		}
	}
}
