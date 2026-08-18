package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	set := Set{CWD: t.TempDir()}
	classic := set.Build(Profile{Editor: EditorWriteEdit})
	if got := strings.Join(names(classic), ","); got != "Read,Write,Edit,Bash" {
		t.Fatalf("classic tools = %s", got)
	}
	textRead := pick(classic, "Read")
	if strings.Contains(textRead.Prompt(), "PDF") || textRead.Parameters()["properties"].(map[string]any)["pages"] != nil {
		t.Fatalf("text Read leaked rich capabilities: %s %+v", textRead.Prompt(), textRead.Parameters())
	}

	patch := set.Build(Profile{RichRead: true, Editor: EditorApplyPatch})
	if got := strings.Join(names(patch), ","); got != "Read,apply_patch,Bash" {
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

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	cwd := t.TempDir()
	b := bashTool{cwd: cwd, jobs: NewJobStore()}
	started := time.Now()
	res := b.Execute(context.Background(), map[string]any{
		"command": "sleep 120 & echo $! > child.pid; wait",
		"timeout": 1000,
	})
	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("timeout took %s, want ~1s", took)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "timed out after 1 seconds") {
		t.Fatalf("want timeout, got %+v", res)
	}
	pid := waitPIDFile(t, filepath.Join(cwd, "child.pid"))
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
