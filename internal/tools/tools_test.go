package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/toolname"
)

func pick(ts []loop.Tool, name string) loop.Tool {
	for _, t := range ts {
		if toolname.Equal(name, t.Name()) {
			return t
		}
	}
	return nil
}

func names(ts []loop.Tool) []string {
	out := make([]string, 0, len(ts))
	for _, tool := range ts {
		out = append(out, tool.Name())
	}
	return out
}

func TestBuildSelectsReadCapabilities(t *testing.T) {
	shells := DiscoverShellRuntime()
	set := Set{CWD: t.TempDir(), Shells: shells}
	classic := set.Build(Profile{})
	wantClassic := []string{"read", "write", "edit", "grep", "glob", "exec_command", "write_stdin"}
	if got := strings.Join(names(classic), ","); got != strings.Join(wantClassic, ",") {
		t.Fatalf("classic tools = %s", got)
	}
	textRead := pick(classic, "Read")
	properties, ok := textRead.Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("text Read properties = %#v", textRead.Parameters()["properties"])
	}
	if strings.Contains(textRead.Prompt(), "PDF") || properties["pages"] != nil {
		t.Fatalf("text Read leaked rich capabilities: %s %+v", textRead.Prompt(), textRead.Parameters())
	}

	patch := set.Build(Profile{RichRead: true, ApplyPatch: true})
	wantPatch := []string{"read", "apply_patch", "grep", "glob", "exec_command", "write_stdin"}
	if got := strings.Join(names(patch), ","); got != strings.Join(wantPatch, ",") {
		t.Fatalf("patch tools = %s", got)
	}
	if !strings.Contains(pick(patch, "Read").Prompt(), "PDF") {
		t.Fatal("rich Read omitted PDF capability")
	}
	provider, ok := pick(patch, "apply_patch").(loop.ToolSpecProvider)
	if !ok {
		t.Fatal("apply_patch does not provide a custom tool spec")
	}
	spec := provider.ToolSpec()
	if spec.Type != "custom" || spec.Format == nil || spec.Format.Syntax != "lark" {
		t.Fatalf("apply_patch spec = %+v", spec)
	}
	if !strings.Contains(spec.Description, "Include each file path once") {
		t.Fatalf("apply_patch description omits single-path rule: %q", spec.Description)
	}
	catalog := set.Catalog(Profile{ApplyPatch: true})
	for _, name := range []string{"apply_patch", "Write", "Edit"} {
		if pick(catalog, name) == nil {
			t.Fatalf("catalog omitted %s: %v", name, names(catalog))
		}
	}
}

func TestFilterBuiltinsHonorsToggle(t *testing.T) {
	store := NewAgentController()
	defer store.Close()
	set := Set{CWD: t.TempDir(), Agent: fakeAgentRuntime{store: store}}
	all := set.Build(Profile{})
	filtered := FilterBuiltins(all, session.Toggle{Disabled: []string{"Read", "SpawnAgent"}})
	if pick(filtered, "Read") != nil {
		t.Fatal("Read was not disabled")
	}
	if pick(filtered, "SpawnAgent") != nil {
		t.Fatal("Agent was not disabled")
	}
	if pick(filtered, "Grep") == nil {
		t.Fatal("unlisted built-in tool was disabled")
	}
}

func TestExecCommandContract(t *testing.T) {
	manager := NewShellProcessManager()
	defer manager.Close()
	tool := pick(Set{CWD: t.TempDir(), Processes: manager}.Build(Profile{}), "ExecCommand")
	props := tool.Parameters()["properties"].(map[string]any)
	for _, name := range []string{"cmd", "workdir", "shell", "login", "tty", "yield_time_ms", "max_output_tokens"} {
		if props[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	for _, name := range []string{"command", "timeout", "run_in_background"} {
		if props[name] != nil {
			t.Fatalf("legacy argument %s", name)
		}
	}
	if tool.(loop.ToolValidator).Validate(map[string]any{"cmd": "printf ok"}) != nil || tool.(loop.ToolValidator).Validate(map[string]any{"command": "printf ok"}) == nil {
		t.Fatal("exec schema mismatch")
	}
}

func TestTextReadRejectsImageAndPDF(t *testing.T) {
	cwd := t.TempDir()
	imagePath := filepath.Join(cwd, "image.png")
	pdfPath := filepath.Join(cwd, "file.pdf")
	_ = os.WriteFile(imagePath, []byte("\x89PNG\r\n\x1a\nbody"), 0o600)
	_ = os.WriteFile(pdfPath, []byte("%PDF-1.4\n(body)"), 0o600)
	read := readTool{cwd: cwd}
	for _, path := range []string{imagePath, pdfPath} {
		if res := read.Execute(context.Background(), map[string]any{"file_path": path}); !res.IsError {
			t.Fatalf("text Read accepted %s: %+v", path, res)
		}
	}
}

func TestReadWriteEditRelativeAndNoLineNumbers(t *testing.T) {
	cwd := t.TempDir()
	set := Set{CWD: cwd}
	all := set.Build(Profile{RichRead: true})
	read, write, edit := pick(all, "Read"), pick(all, "Write"), pick(all, "Edit")
	res := write.Execute(context.Background(), map[string]any{
		"file_path": "a.txt",
		"content":   "hello\nworld\n",
	})
	if res.IsError {
		t.Fatalf("write: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "Successfully wrote") {
		t.Fatalf("write msg: %s", res.Content[0].Text)
	}
	got := read.Execute(context.Background(), map[string]any{"file_path": "a.txt"})
	text := got.Content[0].Text
	if strings.HasPrefix(strings.TrimSpace(text), "1") && strings.Contains(text, "\t") {
		t.Fatalf("should not be cat -n: %q", text)
	}
	if !strings.Contains(text, "hello") {
		t.Fatalf("read: %q", text)
	}
	ed := edit.Execute(context.Background(), map[string]any{
		"file_path":  "a.txt",
		"old_string": "hello",
		"new_string": "hi",
	})
	if ed.IsError {
		t.Fatalf("edit: %+v", ed)
	}
	//nolint:gosec // cwd is an isolated test directory.
	b, _ := os.ReadFile(filepath.Join(cwd, "a.txt"))
	if !strings.HasPrefix(string(b), "hi\n") {
		t.Fatalf("file: %q", b)
	}
}

func TestGrepAndGlobTools(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "src", "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set := Set{CWD: cwd}.Build(Profile{})
	grep, glob := pick(set, "Grep"), pick(set, "Glob")

	grepResult := grep.Execute(context.Background(), map[string]any{
		"pattern":     "func main",
		"path":        "src",
		"output_mode": "content",
	})
	if grepResult.IsError || !strings.Contains(grepResult.Content[0].Text, "main.go:2:") {
		t.Fatalf("grep result = %+v", grepResult)
	}
	filesResult := grep.Execute(context.Background(), map[string]any{"pattern": "func main"})
	if filesResult.IsError || !strings.Contains(filesResult.Content[0].Text, "src/main.go") {
		t.Fatalf("grep files result = %+v", filesResult)
	}
	countResult := grep.Execute(context.Background(), map[string]any{"pattern": "func main", "output_mode": "count"})
	if countResult.IsError || !strings.Contains(countResult.Content[0].Text, "src/main.go:1") {
		t.Fatalf("grep count result = %+v", countResult)
	}

	globResult := glob.Execute(context.Background(), map[string]any{"pattern": "**/*.go"})
	if globResult.IsError || !strings.Contains(globResult.Content[0].Text, "src/main.go") || !strings.Contains(globResult.Content[0].Text, "files: 1") || !strings.Contains(globResult.Content[0].Text, "root:") {
		t.Fatalf("glob result = %+v", globResult)
	}
	_ = os.WriteFile(filepath.Join(cwd, ".gitignore"), []byte("ignored.go\n"), 0o600)
	_ = os.Mkdir(filepath.Join(cwd, ".git"), 0o700)
	_ = os.WriteFile(filepath.Join(cwd, "ignored.go"), []byte("package ignored\n"), 0o600)
	defaultRespected := glob.Execute(context.Background(), map[string]any{"pattern": "**/ignored.go"})
	noIgnore := glob.Execute(context.Background(), map[string]any{"pattern": "**/ignored.go", "respect_gitignore": false})
	if strings.Contains(defaultRespected.Content[0].Text, "ignored.go\n") || !strings.Contains(noIgnore.Content[0].Text, "ignored.go") {
		t.Fatalf("gitignore behavior: default=%q noIgnore=%q", defaultRespected.Content[0].Text, noIgnore.Content[0].Text)
	}
	grepDefault := grep.Execute(context.Background(), map[string]any{"pattern": "package ignored"})
	grepNoIgnore := grep.Execute(context.Background(), map[string]any{"pattern": "package ignored", "respect_gitignore": false})
	if strings.Contains(grepDefault.Content[0].Text, "ignored.go") || !strings.Contains(grepNoIgnore.Content[0].Text, "ignored.go") {
		t.Fatalf("grep gitignore behavior: default=%q noIgnore=%q", grepDefault.Content[0].Text, grepNoIgnore.Content[0].Text)
	}
}

func TestEditReplaceAllAndUniqueFailure(t *testing.T) {
	cwd := t.TempDir()
	p := filepath.Join(cwd, "b.txt")
	_ = os.WriteFile(p, []byte("x x x"), 0o600)
	ed := editTool{cwd: cwd}
	res := ed.Execute(context.Background(), map[string]any{
		"file_path": p, "old_string": "x", "new_string": "y",
	})
	if !res.IsError {
		t.Fatal("expected unique failure")
	}
	res = ed.Execute(context.Background(), map[string]any{
		"file_path": p, "old_string": "x", "new_string": "y", "replace_all": true,
	})
	if res.IsError {
		t.Fatalf("%+v", res)
	}
	b, _ := os.ReadFile(p) //nolint:gosec // path is inside the isolated test directory
	if string(b) != "y y y" {
		t.Fatalf("got %q", b)
	}
}

func TestEditBatchUsesOneOriginalAndReturnsDiffDetails(t *testing.T) {
	cwd := t.TempDir()
	p := filepath.Join(cwd, "batch.txt")
	_ = os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o600)
	ed := editTool{cwd: cwd, mutations: NewMutationQueue()}
	args := map[string]any{"file_path": p, "edits": []any{
		map[string]any{"old_string": "alpha", "new_string": "ALPHA"},
		map[string]any{"old_string": "gamma", "new_string": "GAMMA"},
	}}
	if err := ed.Validate(args); err != nil {
		t.Fatal(err)
	}
	res := ed.Execute(context.Background(), args)
	if res.IsError || !strings.Contains(res.Content[0].Text, "2 block") {
		t.Fatalf("batch edit: %+v", res)
	}
	details, ok := res.Details.(editDetails)
	if !ok || !strings.Contains(details.Patch, "-alpha") || !strings.Contains(details.Patch, "+GAMMA") || details.FirstChangedLine != 1 {
		t.Fatalf("details: %#v", res.Details)
	}
	b, _ := os.ReadFile(p) //nolint:gosec // path is inside the isolated test directory
	if string(b) != "ALPHA\nbeta\nGAMMA\n" {
		t.Fatalf("file: %q", b)
	}

	mixed := map[string]any{"file_path": p, "old_string": "beta", "new_string": "BETA", "edits": []any{map[string]any{"old_string": "beta", "new_string": "B"}}}
	if err := ed.Validate(mixed); err == nil {
		t.Fatal("mixed edit modes must fail")
	}
}

func TestEditSelectsSoleMeaningfulModeAndWarns(t *testing.T) {
	cwd := t.TempDir()
	ed := editTool{cwd: cwd, mutations: NewMutationQueue()}

	t.Run("batch ignores default single fields", func(t *testing.T) {
		path := filepath.Join(cwd, "batch-defaults.txt")
		_ = os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600)
		args := map[string]any{
			"file_path": path,
			"edits": []any{
				map[string]any{"old_string": "alpha", "new_string": "ALPHA"},
			},
			"old_string": "", "new_string": "", "replace_all": false,
		}
		if err := ed.Validate(args); err != nil {
			t.Fatal(err)
		}
		res := ed.Execute(context.Background(), args)
		if res.IsError || !strings.Contains(res.Content[0].Text, "ignored fields from the inactive Edit mode") {
			t.Fatalf("batch fallback: %+v", res)
		}
		details, ok := res.Details.(editDetails)
		if !ok || strings.Join(details.IgnoredFields, ",") != "old_string,new_string,replace_all" {
			t.Fatalf("ignored fields: %#v", res.Details)
		}
		got, _ := os.ReadFile(path) //nolint:gosec // path is inside the isolated test directory
		if string(got) != "ALPHA\nbeta\n" {
			t.Fatalf("file: %q", got)
		}
	})

	t.Run("single ignores no-op batch placeholder", func(t *testing.T) {
		path := filepath.Join(cwd, "single-placeholder.txt")
		_ = os.WriteFile(path, []byte("alpha\n"), 0o600)
		args := map[string]any{
			"file_path":  path,
			"old_string": "alpha", "new_string": "ALPHA", "replace_all": false,
			"edits": []any{map[string]any{"old_string": "placeholder", "new_string": "placeholder"}},
		}
		if err := ed.Validate(args); err != nil {
			t.Fatal(err)
		}
		res := ed.Execute(context.Background(), args)
		if res.IsError || !strings.Contains(res.Content[0].Text, "ignored fields from the inactive Edit mode: edits") {
			t.Fatalf("single fallback: %+v", res)
		}
		got, _ := os.ReadFile(path) //nolint:gosec // path is inside the isolated test directory
		if string(got) != "ALPHA\n" {
			t.Fatalf("file: %q", got)
		}
	})
}

func TestReadPagingAndImageResize(t *testing.T) {
	cwd := t.TempDir()
	textPath := filepath.Join(cwd, "lines.txt")
	_ = os.WriteFile(textPath, []byte("one\ntwo\nthree\n"), 0o600)
	read := readTool{cwd: cwd, rich: true}
	page := read.Execute(context.Background(), map[string]any{"file_path": textPath, "offset": 2, "limit": 1})
	if page.IsError || !strings.HasPrefix(page.Content[0].Text, "two\n") {
		t.Fatalf("line page: %+v", page)
	}
	params := read.Parameters()["properties"].(map[string]any)
	if params["byte_offset"] != nil || params["byte_limit"] != nil {
		t.Fatal("Read schema still exposes byte paging")
	}

	imagePath := filepath.Join(cwd, "wide.png")
	img := image.NewNRGBA(image.Rect(0, 0, 2100, 2))
	f, _ := os.Create(imagePath) //nolint:gosec // path is inside the isolated test directory
	_ = png.Encode(f, img)
	_ = f.Close()
	imageResult := read.Execute(context.Background(), map[string]any{"file_path": imagePath})
	imageDetailsValue, ok := imageResult.Details.(readDetails)
	if !ok {
		t.Fatalf("image details = %#v", imageResult.Details)
	}
	imageInfo := imageDetailsValue.Image
	if imageResult.IsError || !imageInfo.Resized || imageInfo.Width > maxImageDimension {
		t.Fatalf("image resize: %+v", imageResult)
	}
}

func TestMutationQueueSerializesSamePathAndCancelsWait(t *testing.T) {
	q := NewMutationQueue()
	path := filepath.Join(t.TempDir(), "a")
	release, err := q.LockPaths(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := q.LockPaths(ctx, path); done <- err }()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled waiter acquired the path")
	}
	release()
	otherRelease, err := q.LockPaths(context.Background(), path+"-other")
	if err != nil {
		t.Fatal(err)
	}
	otherRelease()
}

func TestOutputSanitizerHandlesSplitANSIAndControls(t *testing.T) {
	var sanitizer outputSanitizer
	first := sanitizer.Filter([]byte("a\x1b[3"))
	second := sanitizer.Filter([]byte("1mred\x1b[0m\x01\t\n"))
	if got := string(append(first, second...)); got != "ared\t\n" {
		t.Fatalf("sanitized = %q", got)
	}
}

func testExec(t *testing.T, spool OutputSpool) execCommandTool {
	t.Helper()
	manager := NewSpooledShellProcessManager(spool, "test-session")
	t.Cleanup(manager.Close)
	shells := DiscoverShellRuntime()
	if !shells.BashAvailable() {
		t.Skip("Bash unavailable for POSIX command fixtures")
	}
	shells.powerShell = nil
	return execCommandTool{cwd: t.TempDir(), processes: manager, shells: shells}
}
func execSnapshot(t *testing.T, result loop.ToolResult) ProcessSnapshot {
	t.Helper()
	var snap ProcessSnapshot
	if len(result.Content) == 0 {
		t.Fatal("no process result")
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &snap); err != nil {
		t.Fatalf("decode: %v result=%+v", err, result)
	}
	return snap
}
func TestExecNonZeroAndCwdReset(t *testing.T) {
	tool := testExec(t, nil)
	result := tool.Execute(t.Context(), map[string]any{"cmd": "exit 3"})
	snap := execSnapshot(t, result)
	if !result.IsError || snap.ExitCode == nil || *snap.ExitCode != 3 {
		t.Fatalf("nonzero: %+v", result)
	}
	child := filepath.Join(tool.cwd, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	snap = execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "pwd", "workdir": child}))
	if !strings.Contains(snap.Output, "child") {
		t.Fatalf("cwd: %+v", snap)
	}
	snap = execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "pwd"}))
	if strings.Contains(snap.Output, "child") {
		t.Fatalf("cwd persisted: %+v", snap)
	}
}
func TestExecObserverCancelPreservesProcessAndExplicitStopKillsTree(t *testing.T) {
	tool := testExec(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan loop.ToolResult, 1)
	go func() { done <- tool.Execute(ctx, map[string]any{"cmd": "sleep 120 & echo $! > child.pid; wait"}) }()
	pidPath := filepath.Join(tool.cwd, "child.pid")
	var pid int
	waitFor(t, func() bool {
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		return pid > 0
	}, "child did not start")
	cancel()
	result := <-done
	snap := execSnapshot(t, result)
	if !result.IsError || snap.Status != "running" {
		t.Fatalf("observer cancellation killed process: %+v", result)
	}
	probe := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": fmt.Sprintf("kill -0 %d 2>/dev/null && echo alive", pid)}))
	if !strings.Contains(probe.Output, "alive") {
		t.Fatal("descendant no longer alive")
	}
	stopped, err := tool.processes.Terminate(snap.SessionID)
	if err != nil || stopped.Status != "exited" {
		t.Fatalf("terminate: %+v %v", stopped, err)
	}
	waitFor(t, func() bool {
		probe := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": fmt.Sprintf("kill -0 %d 2>/dev/null || echo gone", pid)}))
		return strings.Contains(probe.Output, "gone")
	}, "descendant survived tree termination")
}
func TestExecYieldAndIncrementalOutput(t *testing.T) {
	tool := testExec(t, nil)
	first := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "printf 'one\n'; sleep 0.6; printf 'two\n'", "yield_time_ms": 250}))
	if first.Status != "running" || first.Output != "one\n" {
		t.Fatalf("initial yield: %+v", first)
	}
	next := execSnapshot(t, (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": first.SessionID, "yield_time_ms": 5000}))
	if next.Status != "exited" || next.Output != "two\n" || next.OutputOffset != 4 {
		t.Fatalf("incremental result: %+v", next)
	}
	again := execSnapshot(t, (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": first.SessionID}))
	if again.Output != "" {
		t.Fatalf("output consumed twice: %+v", again)
	}
	raw, err := os.ReadFile(first.OutputFile)
	if err != nil || string(raw) != "one\ntwo\n" {
		t.Fatalf("spool: %q %v", raw, err)
	}
}
func TestExecLargeOutputSpoolsAndReadPages(t *testing.T) {
	tool := testExec(t, nil)
	snap := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "i=1; while [ $i -le 8000 ]; do printf 'line-%05d\n' \"$i\"; i=$((i+1)); done", "max_output_tokens": 32}))
	if !snap.Truncated || len(snap.Output) > 128 || !strings.Contains(snap.Output, "line-00001") {
		t.Fatalf("bounded output: %+v", snap)
	}
	raw, err := os.ReadFile(snap.OutputFile)
	if err != nil || !strings.Contains(string(raw), "line-00001\n") || !strings.Contains(string(raw), "line-08000\n") {
		t.Fatalf("incomplete spool: %v", err)
	}
	result := (readTool{cwd: tool.cwd}).Execute(t.Context(), map[string]any{"file_path": snap.OutputFile, "offset": 1, "limit": 2})
	if result.IsError || !strings.Contains(result.Content[0].Text, "line-00001\nline-00002") {
		t.Fatalf("Read spool: %+v", result)
	}
	next := execSnapshot(t, (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": snap.SessionID, "max_output_tokens": 32}))
	if next.OutputOffset != int64(len(snap.Output)) || next.Output == snap.Output {
		t.Fatalf("cursor: %+v", next)
	}
}
func TestExecSmallOutputAndFinishedStop(t *testing.T) {
	tool := testExec(t, nil)
	result := tool.Execute(t.Context(), map[string]any{"cmd": "printf small"})
	snap := execSnapshot(t, result)
	if result.IsError || snap.Output != "small" || snap.Truncated {
		t.Fatalf("small: %+v", result)
	}
	stopped, err := tool.processes.Terminate(snap.SessionID)
	if err != nil || stopped.Status != "exited" {
		t.Fatalf("finished stop: %+v %v", stopped, err)
	}
}
func TestWriteStdinPipeAndPTY(t *testing.T) {
	tool := testExec(t, nil)
	pipe := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "sleep 120", "yield_time_ms": 250}))
	rejected := (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": pipe.SessionID, "chars": "ordinary input\n"})
	if !rejected.IsError {
		t.Fatal("pipe accepted ordinary input")
	}
	if _, err := tool.processes.Terminate(pipe.SessionID); err != nil {
		t.Fatal(err)
	}
	tty := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "read value; printf 'received:%s' \"$value\"", "tty": true, "yield_time_ms": 250}))
	input := (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": tty.SessionID, "chars": "hello\n", "yield_time_ms": 2000})
	end := execSnapshot(t, input)
	if input.IsError || end.Status != "exited" || !strings.Contains(end.Output, "received:hello") {
		t.Fatalf("PTY: %+v", input)
	}
}
func TestProcessCapacityAndSessionIsolation(t *testing.T) {
	tool := testExec(t, nil)
	tool.processes.limit = 1
	snap := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "sleep 120", "yield_time_ms": 250}))
	if result := tool.Execute(t.Context(), map[string]any{"cmd": "printf no"}); !result.IsError {
		t.Fatal("live capacity was not enforced")
	}
	other := NewShellProcessManager()
	defer other.Close()
	if _, err := other.Interact(t.Context(), snap.SessionID, "", time.Millisecond, 10, nil); err == nil {
		t.Fatal("another session accessed process")
	}
	if _, err := tool.processes.Terminate(snap.SessionID); err != nil {
		t.Fatal(err)
	}
	if result := tool.Execute(t.Context(), map[string]any{"cmd": "printf yes"}); result.IsError {
		t.Fatal("finished process still occupies live capacity")
	}
}
func TestTruncateTailBoundsLongUTF8Line(t *testing.T) {
	out, note := truncateTail(strings.Repeat("你", maxBytes))
	if len(out) > maxBytes || !utf8.ValidString(out) || note == "" {
		t.Fatal("UTF-8 tail truncation contract")
	}
}

func TestReadNotebookAndPDFPages(t *testing.T) {
	cwd := t.TempDir()
	r := readTool{cwd: cwd, rich: true}
	nb := `{"cells":[{"cell_type":"code","source":["print(1)\n"],"outputs":[]}]}`
	_ = os.WriteFile(filepath.Join(cwd, "n.ipynb"), []byte(nb), 0o600)
	res := r.Execute(context.Background(), map[string]any{"file_path": "n.ipynb"})
	if !strings.Contains(res.Content[0].Text, "cell 0") {
		t.Fatalf("ipynb: %s", res.Content[0].Text)
	}
	_ = os.WriteFile(filepath.Join(cwd, "a.pdf"), []byte("%PDF-1.4\n(HelloPDF)\n"), 0o600)
	res = r.Execute(context.Background(), map[string]any{"file_path": "a.pdf", "pages": "1-2"})
	if !strings.Contains(res.Content[0].Text, "pages=1-2") {
		t.Fatalf("pdf pages: %s", res.Content[0].Text)
	}

	marker := "KI-PDF-MARKER-42"
	stream := "BT /F1 18 Tf 20 60 Td (" + marker + ") Tj ET\n"
	pdfContent := "%PDF-1.1\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"4 0 obj<</Length " + strconv.Itoa(len(stream)) + ">>stream\n" + stream + "endstream\nendobj\n"
	_ = os.WriteFile(filepath.Join(cwd, "real.pdf"), []byte(pdfContent), 0o600)
	res = r.Execute(context.Background(), map[string]any{"file_path": "real.pdf"})
	if !strings.Contains(res.Content[0].Text, marker) {
		t.Fatalf("real pdf extract: %s", res.Content[0].Text)
	}
}

// The model-visible bound moved to the loop spill, so Grep must hand over the
// complete result instead of cutting it at 20KB; otherwise the spill file would
// only ever contain an already-truncated page.
func TestGrepKeepsCompleteResultForTheSpool(t *testing.T) {
	cwd := t.TempDir()
	var content strings.Builder
	for i := 0; i < 120; i++ {
		content.WriteString(fmt.Sprintf("match %03d %s-end-marker\n", i, strings.Repeat("x", 300)))
	}
	if err := os.WriteFile(filepath.Join(cwd, "big.txt"), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	grep := grepTool{cwd: cwd}
	res := grep.Execute(context.Background(), map[string]any{
		"pattern":     "match",
		"output_mode": "content",
		"head_limit":  0,
	})
	if res.IsError {
		t.Fatalf("grep: %+v", res)
	}
	text := res.Content[0].Text
	if len(text) <= 20_000 {
		t.Fatalf("grep result is %d bytes; the 20KB tool-level cap is back", len(text))
	}
	if !strings.Contains(text, "119") || !strings.Contains(text, "-end-marker") {
		t.Fatalf("grep result dropped matches: %q", text[len(text)-200:])
	}
	if strings.Contains(text, "20000 byte limit") {
		t.Fatalf("grep still applies the removed 20KB limit")
	}
}

type fakeSpool struct {
	dir string
	err error
}

func (s fakeSpool) CreateOutputFile(string, string) (*os.File, error) {
	if s.err != nil {
		return nil, s.err
	}
	return os.CreateTemp(s.dir, "spool-*.log")
}

func TestShellProcessSpoolAndFallback(t *testing.T) {
	dir := t.TempDir()
	tool := testExec(t, fakeSpool{dir: dir})
	snap := execSnapshot(t, tool.Execute(t.Context(), map[string]any{"cmd": "printf spooled-out"}))
	if filepath.Dir(snap.OutputFile) != dir {
		t.Fatalf("output outside session spool: %s", snap.OutputFile)
	}
	raw, err := os.ReadFile(snap.OutputFile)
	if err != nil || string(raw) != "spooled-out" {
		t.Fatalf("spool: %q %v", raw, err)
	}
	fallback := testExec(t, fakeSpool{err: os.ErrPermission})
	res := fallback.Execute(t.Context(), map[string]any{"cmd": "printf fallback"})
	if res.IsError {
		t.Fatalf("spool refusal broke execution: %+v", res)
	}
	path := execSnapshot(t, res).OutputFile
	fallback.processes.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fallback log not cleaned: %v", err)
	}
}
