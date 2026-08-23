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
)

func pick(ts []loop.Tool, name string) loop.Tool {
	for _, t := range ts {
		if t.Name() == name {
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

func TestBuildSelectsReadAndEditorCapabilities(t *testing.T) {
	shells := DiscoverShellRuntime()
	set := Set{CWD: t.TempDir(), Shells: shells}
	classic := set.Build(Profile{Editor: EditorWriteEdit})
	wantClassic := []string{"Read", "Write", "Edit", "Grep", "Glob"}
	if shells.BashAvailable() {
		wantClassic = append(wantClassic, "Bash")
	}
	if shells.PowerShellEnabled() {
		wantClassic = append(wantClassic, "PowerShell")
	}
	wantClassic = append(wantClassic, "TaskOutput", "TaskStop")
	if shells.BashAvailable() {
		wantClassic = append(wantClassic, "Monitor")
	}
	if got := strings.Join(names(classic), ","); got != strings.Join(wantClassic, ",") {
		t.Fatalf("classic tools = %s", got)
	}
	textRead := pick(classic, "Read")
	if strings.Contains(textRead.Prompt(), "PDF") || textRead.Parameters()["properties"].(map[string]any)["pages"] != nil {
		t.Fatalf("text Read leaked rich capabilities: %s %+v", textRead.Prompt(), textRead.Parameters())
	}

	patch := set.Build(Profile{RichRead: true, Editor: EditorApplyPatch})
	wantPatch := []string{"Read", "apply_patch", "Grep", "Glob"}
	if shells.BashAvailable() {
		wantPatch = append(wantPatch, "Bash")
	}
	if shells.PowerShellEnabled() {
		wantPatch = append(wantPatch, "PowerShell")
	}
	wantPatch = append(wantPatch, "TaskOutput", "TaskStop")
	if shells.BashAvailable() {
		wantPatch = append(wantPatch, "Monitor")
	}
	if got := strings.Join(names(patch), ","); got != strings.Join(wantPatch, ",") {
		t.Fatalf("patch tools = %s", got)
	}
	if !strings.Contains(pick(patch, "Read").Prompt(), "PDF") {
		t.Fatal("rich Read omitted PDF capability")
	}
	spec := pick(patch, "apply_patch").(loop.ToolSpecProvider).ToolSpec()
	if spec.Type != "custom" || spec.Format == nil || spec.Format.Syntax != "lark" {
		t.Fatalf("apply_patch spec = %+v", spec)
	}
}

func TestBuildPowerShellContract(t *testing.T) {
	ps := shellSpec{kind: shellPowerShell, path: "pwsh", powerShellEdition: powerShellCore}
	shells := ShellRuntime{bash: shellSpec{kind: shellBash}, powerShell: &ps}
	all := Set{CWD: t.TempDir(), Shells: shells}.Build(Profile{Editor: EditorWriteEdit})
	if pick(all, "Bash") != nil || pick(all, "Monitor") != nil {
		t.Fatalf("Bash-dependent tools registered without Bash: %v", names(all))
	}
	tool := pick(all, "PowerShell")
	if tool == nil {
		t.Fatal("PowerShell was not registered")
	}
	props := tool.Parameters()["properties"].(map[string]any)
	for _, name := range []string{"command", "timeout", "description", "run_in_background"} {
		if props[name] == nil {
			t.Fatalf("PowerShell missing %s schema", name)
		}
	}
	if strings.Contains(tool.Prompt(), "dangerouslyDisableSandbox") || !strings.Contains(tool.Prompt(), "PowerShell 7+") || !strings.Contains(tool.Prompt(), "is not remembered") {
		t.Fatalf("PowerShell prompt = %s", tool.Prompt())
	}

	unavailable := powerShellTool{shell: shellSpec{kind: shellPowerShell}}
	result := unavailable.Execute(context.Background(), map[string]any{"command": "Get-Location"})
	if result.IsError || !strings.Contains(result.Content[0].Text, "not available") {
		t.Fatalf("unavailable result = %+v", result)
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

func TestApplyPatchAddUpdateMoveDelete(t *testing.T) {
	cwd := t.TempDir()
	tool := applyPatchTool{cwd: cwd}
	add := tool.ExecuteRaw(context.Background(), "*** Begin Patch\n*** Add File: a.txt\n+one\n+two\n*** End Patch")
	if add.IsError {
		t.Fatalf("add: %+v", add)
	}
	update := tool.ExecuteRaw(context.Background(), "*** Begin Patch\n*** Update File: a.txt\n@@\n-one\n+ONE\n two\n*** Move to: ignored.txt\n*** End Patch")
	if !update.IsError {
		t.Fatal("move marker after chunks must be rejected")
	}
	move := tool.ExecuteRaw(context.Background(), "*** Begin Patch\n*** Update File: a.txt\n*** Move to: sub/b.txt\n@@\n-one\n+ONE\n two\n*** End Patch")
	if move.IsError {
		t.Fatalf("move: %+v", move)
	}
	b, err := os.ReadFile(filepath.Join(cwd, "sub", "b.txt"))
	if err != nil || string(b) != "ONE\ntwo\n" {
		t.Fatalf("moved file = %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	del := tool.ExecuteRaw(context.Background(), "*** Begin Patch\n*** Delete File: sub/b.txt\n*** End Patch")
	if del.IsError {
		t.Fatalf("delete: %+v", del)
	}
}

func TestApplyPatchNormalizesCRLFAndMatchesWhitespace(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "a.txt")
	if err := os.WriteFile(path, []byte("  one  \r\ntwo\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := (applyPatchTool{cwd: cwd}).ExecuteRaw(context.Background(), "*** Begin Patch\n*** Update File: a.txt\n@@\n-one\n+ONE\n two\n*** End Patch")
	if res.IsError {
		t.Fatalf("patch: %+v", res)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "ONE\ntwo\n" {
		t.Fatalf("normalized file = %q", b)
	}
}

func TestApplyPatchRejectsMalformedInputWithoutWriting(t *testing.T) {
	cwd := t.TempDir()
	res := (applyPatchTool{cwd: cwd}).ExecuteRaw(context.Background(), "*** Begin Patch\n*** Add File: bad.txt\nmissing plus\n*** End Patch")
	if !res.IsError || !strings.Contains(res.Content[0].Text, "verification failed") {
		t.Fatalf("malformed patch = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(cwd, "bad.txt")); !os.IsNotExist(err) {
		t.Fatalf("malformed patch wrote file: %v", err)
	}
}

func TestReadWriteEditRelativeAndNoLineNumbers(t *testing.T) {
	cwd := t.TempDir()
	set := Set{CWD: cwd, Jobs: NewJobStore()}
	all := set.Build(Profile{RichRead: true, Editor: EditorWriteEdit})
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
	set := Set{CWD: cwd}.Build(Profile{Editor: EditorWriteEdit})
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
	defaultIgnored := glob.Execute(context.Background(), map[string]any{"pattern": "**/ignored.go"})
	respected := glob.Execute(context.Background(), map[string]any{"pattern": "**/ignored.go", "respect_gitignore": true})
	if !strings.Contains(defaultIgnored.Content[0].Text, "ignored.go") || strings.Contains(respected.Content[0].Text, "ignored.go\n") {
		t.Fatalf("gitignore behavior: default=%q respected=%q", defaultIgnored.Content[0].Text, respected.Content[0].Text)
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
	//nolint:gosec // p is created inside the isolated test directory.
	b, _ := os.ReadFile(p)
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
	b, _ := os.ReadFile(p)
	if string(b) != "ALPHA\nbeta\nGAMMA\n" {
		t.Fatalf("file: %q", b)
	}

	mixed := map[string]any{"file_path": p, "old_string": "beta", "new_string": "BETA", "edits": []any{map[string]any{"old_string": "beta", "new_string": "B"}}}
	if err := ed.Validate(mixed); err == nil {
		t.Fatal("mixed edit modes must fail")
	}
}

func TestReadBytePagingAndImageResize(t *testing.T) {
	cwd := t.TempDir()
	textPath := filepath.Join(cwd, "long.txt")
	_ = os.WriteFile(textPath, []byte(strings.Repeat("你", 20000)), 0o600)
	read := readTool{cwd: cwd, rich: true}
	first := read.Execute(context.Background(), map[string]any{"file_path": textPath})
	details := first.Details.(readDetails).Truncation
	if !details.Truncated || details.NextByteOffset == 0 || !utf8.ValidString(first.Content[0].Text) {
		t.Fatalf("first page: %+v", first)
	}
	next := read.Execute(context.Background(), map[string]any{"file_path": textPath, "byte_offset": details.NextByteOffset})
	if next.IsError || !utf8.ValidString(next.Content[0].Text) {
		t.Fatalf("byte page: %+v", next)
	}

	imagePath := filepath.Join(cwd, "wide.png")
	img := image.NewNRGBA(image.Rect(0, 0, 2100, 2))
	f, _ := os.Create(imagePath)
	_ = png.Encode(f, img)
	_ = f.Close()
	imageResult := read.Execute(context.Background(), map[string]any{"file_path": imagePath})
	imageInfo := imageResult.Details.(readDetails).Image
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
	ctx, cancel := context.WithCancel(context.Background())
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

func TestBashNonZeroIsErrorAndCwdReset(t *testing.T) {
	cwd := t.TempDir()
	b := bashTool{cwd: cwd, jobs: NewJobStore()}
	res := b.Execute(context.Background(), map[string]any{"command": "exit 7"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "exited with code 7") {
		t.Fatalf("nonzero: %+v", res)
	}
	_ = os.WriteFile(filepath.Join(cwd, "here.txt"), []byte("ok"), 0o600)
	res = b.Execute(context.Background(), map[string]any{"command": "cd / && cat here.txt"})
	if !res.IsError {
		t.Fatal("cat from / should fail; cwd must reset next command")
	}
	res = b.Execute(context.Background(), map[string]any{"command": "cat here.txt"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "ok") {
		t.Fatalf("cwd should reset to session: %+v", res)
	}
}

func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid file %s not written", path)
	return 0
}

func assertPIDDead(t *testing.T, b bashTool, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		res := b.Execute(context.Background(), map[string]any{
			"command": fmt.Sprintf("kill -0 %d", pid),
		})
		if res.IsError {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after cancel/timeout", pid)
}

func TestBashCancelKillsProcessGroup(t *testing.T) {
	cwd := t.TempDir()
	b := bashTool{cwd: cwd, jobs: NewJobStore()}
	pidPath := filepath.Join(cwd, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan loop.ToolResult, 1)
	go func() {
		done <- b.Execute(ctx, map[string]any{
			"command": "sleep 120 & echo $! > child.pid; wait",
		})
	}()
	pid := waitPIDFile(t, pidPath)
	cancel()
	select {
	case res := <-done:
		if !res.IsError || !strings.Contains(res.Content[0].Text, "Command aborted") {
			t.Fatalf("want aborted, got %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after cancel (stdout Wait hang?)")
	}
	assertPIDDead(t, b, pid)
}

func TestBashTimeoutPromotesToBackground(t *testing.T) {
	cwd := t.TempDir()
	jobs := NewJobStore()
	defer jobs.Close()
	b := bashTool{cwd: cwd, jobs: jobs}
	started := time.Now()
	res := b.Execute(context.Background(), map[string]any{
		"command": "true; sleep 120 & echo $! > child.pid; wait",
		"timeout": 1000,
	})
	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("timeout took %s, want ~1s", took)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "continues in background") {
		t.Fatalf("want background promotion, got %+v", res)
	}
	pid := waitPIDFile(t, filepath.Join(cwd, "child.pid"))
	var taskID string
	for candidate := range jobs.jobs {
		if strings.HasPrefix(candidate, "bg-") {
			if snapshot, ok := jobs.Get(candidate); ok && snapshot.Status == TaskBackground {
				taskID = candidate
				break
			}
		}
	}
	if taskID == "" {
		t.Fatal("background task was not registered")
	}
	if _, err := jobs.Stop(taskID); err != nil {
		t.Fatal(err)
	}
	assertPIDDead(t, b, pid)
}

func TestBashBackgroundAndRead(t *testing.T) {
	cwd := t.TempDir()
	jobs := NewJobStore()
	set := Set{CWD: cwd, Jobs: jobs}
	all := set.Build(Profile{Editor: EditorWriteEdit})
	bash, read := pick(all, "Bash"), pick(all, "Read")
	res := bash.Execute(context.Background(), map[string]any{
		"command":           "echo bg-out",
		"run_in_background": true,
	})
	if res.IsError {
		t.Fatalf("%+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "output_file:") {
		t.Fatalf("bg result: %s", text)
	}
	// wait briefly for the command
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		_, after, _ := strings.Cut(text, "output_file: ")
		path := strings.TrimSpace(strings.Split(after, "\n")[0])
		r := read.Execute(context.Background(), map[string]any{"file_path": path})
		got = r.Content[0].Text
		if strings.Contains(got, "bg-out") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("bg output: %q", got)
}

func TestBashForegroundTruncationSpillsAndReadCanPage(t *testing.T) {
	cwd := t.TempDir()
	jobs := NewJobStore()
	defer jobs.Close()
	all := Set{CWD: cwd, Jobs: jobs}.Build(Profile{Editor: EditorWriteEdit})
	bash, read := pick(all, "Bash"), pick(all, "Read")
	result := bash.Execute(context.Background(), map[string]any{
		"command": "i=1; while [ $i -le 3000 ]; do printf 'line-%04d\\n' \"$i\"; i=$((i+1)); done",
	})
	if result.IsError {
		t.Fatalf("foreground bash: %+v", result)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "line-0001") || !strings.Contains(text, "line-3000") {
		t.Fatalf("result should contain only the output tail: %q", text)
	}
	_, suffix, ok := strings.Cut(text, "Full output: ")
	if !ok {
		t.Fatalf("truncated result omitted full output path: %q", text)
	}
	path := strings.TrimSuffix(strings.TrimSpace(suffix), "]")
	if !filepath.IsAbs(path) {
		t.Fatalf("full output path is not absolute: %q", path)
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(full), "line-0001\n") || !strings.Contains(string(full), "line-3000\n") {
		t.Fatalf("spill file is incomplete: first/last lines missing")
	}

	page := read.Execute(context.Background(), map[string]any{"file_path": path, "offset": 1, "limit": 2})
	if page.IsError || !strings.Contains(page.Content[0].Text, "line-0001\nline-0002") {
		t.Fatalf("Read could not page spill file from its beginning: %+v", page)
	}
}

func TestBashForegroundSmallOutputDoesNotExposeSpill(t *testing.T) {
	jobs := NewJobStore()
	defer jobs.Close()
	bash := bashTool{cwd: t.TempDir(), jobs: jobs}
	result := bash.Execute(context.Background(), map[string]any{"command": "printf small"})
	if result.IsError || result.Content[0].Text != "small" {
		t.Fatalf("small foreground result: %+v", result)
	}
	if strings.Contains(result.Content[0].Text, "Full output:") {
		t.Fatalf("untruncated result exposed spill path: %q", result.Content[0].Text)
	}
}

func TestTruncateTailBoundsLongUTF8Line(t *testing.T) {
	input := strings.Repeat("你", maxBytes)
	out, note := truncateTail(input)
	if len(out) > maxBytes {
		t.Fatalf("truncated output has %d bytes, limit %d", len(out), maxBytes)
	}
	if !utf8.ValidString(out) {
		t.Fatal("truncation split a UTF-8 code point")
	}
	if note == "" {
		t.Fatal("long line omitted truncation note")
	}
}

func TestTaskOutputAndTaskStopLifecycle(t *testing.T) {
	cwd := t.TempDir()
	jobs := NewJobStore()
	defer jobs.Close()
	all := Set{CWD: cwd, Jobs: jobs}.Build(Profile{Editor: EditorWriteEdit})
	bash := pick(all, "Bash")
	outputTool := pick(all, "TaskOutput")
	stopTool := pick(all, "TaskStop")
	started := bash.Execute(context.Background(), map[string]any{
		"command":           "printf 'one\\n'; sleep 0.05; printf 'two\\n'",
		"run_in_background": true,
	})
	if started.IsError {
		t.Fatalf("start: %+v", started)
	}
	id := strings.Fields(strings.Split(started.Content[0].Text, "\n")[0])[3]
	result := outputTool.Execute(context.Background(), map[string]any{
		"task_id": id, "block": true, "timeout": 2000,
	})
	if result.IsError {
		t.Fatalf("output: %+v", result)
	}
	var response struct {
		RetrievalStatus string       `json:"retrieval_status"`
		Task            TaskSnapshot `json:"task"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &response); err != nil {
		t.Fatal(err)
	}
	if response.RetrievalStatus != "success" || response.Task.Status != TaskCompleted || response.Task.Output != "one\ntwo\n" {
		t.Fatalf("task output = %+v", response)
	}

	started = bash.Execute(context.Background(), map[string]any{
		"command":           "sleep 120",
		"run_in_background": true,
	})
	id = strings.Fields(strings.Split(started.Content[0].Text, "\n")[0])[3]
	stopped := stopTool.Execute(context.Background(), map[string]any{"task_id": id})
	if stopped.IsError || !strings.Contains(stopped.Content[0].Text, "stopped") {
		t.Fatalf("stop: %+v", stopped)
	}
	result = outputTool.Execute(context.Background(), map[string]any{"task_id": id, "block": false})
	if result.IsError || !strings.Contains(result.Content[0].Text, `"status":"killed"`) {
		t.Fatalf("stopped output: %+v", result)
	}
}

func TestTaskOutputKeepsLargeLogsOnDisk(t *testing.T) {
	jobs := NewJobStore()
	defer jobs.Close()
	all := Set{CWD: t.TempDir(), Jobs: jobs}.Build(Profile{Editor: EditorWriteEdit})
	bash, outputTool := pick(all, "Bash"), pick(all, "TaskOutput")
	started := bash.Execute(context.Background(), map[string]any{"command": "i=1; while [ $i -le 8000 ]; do printf 'task-%05d\\n' \"$i\"; i=$((i+1)); done", "run_in_background": true})
	id := strings.Fields(strings.Split(started.Content[0].Text, "\n")[0])[3]
	result := outputTool.Execute(context.Background(), map[string]any{"task_id": id, "block": true, "timeout": 5000})
	if result.IsError || len(result.Content[0].Text) > maxBytes+5000 || strings.Contains(result.Content[0].Text, "task-00001") || !strings.Contains(result.Content[0].Text, "task-08000") {
		t.Fatalf("bounded TaskOutput: size=%d result=%+v", len(result.Content[0].Text), result)
	}
	details, ok := result.Details.(taskDetails)
	if !ok || details.Truncation == nil || !details.Truncation.Truncated || details.OutputFile == "" {
		t.Fatalf("task details: %#v", result.Details)
	}
}

func TestMonitorStreamsOutput(t *testing.T) {
	jobs := NewJobStore()
	defer jobs.Close()
	monitor := monitorTool{cwd: t.TempDir(), jobs: jobs}
	var updates []string
	result := monitor.ExecuteWithProgress(context.Background(), map[string]any{
		"command": "printf 'first\\n'; sleep 0.05; printf 'second\\n'",
	}, func(value any) {
		if progress, ok := value.(map[string]any); ok {
			if text, ok := progress["output"].(string); ok {
				updates = append(updates, text)
			}
		}
	})
	if result.IsError || !strings.Contains(result.Content[0].Text, `"status":"completed"`) {
		t.Fatalf("monitor: %+v", result)
	}
	if !strings.Contains(strings.Join(updates, ""), "first") || !strings.Contains(strings.Join(updates, ""), "second") {
		t.Fatalf("monitor updates = %q", updates)
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
