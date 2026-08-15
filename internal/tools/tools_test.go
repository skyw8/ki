package tools

import (
	"context"
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

func TestReadWriteEditRelativeAndNoLineNumbers(t *testing.T) {
	cwd := t.TempDir()
	set := Set{CWD: cwd, Jobs: NewJobStore()}
	all := set.All()
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
	b, _ := os.ReadFile(filepath.Join(cwd, "a.txt"))
	if !strings.HasPrefix(string(b), "hi\n") {
		t.Fatalf("file: %q", b)
	}
}

func TestEditReplaceAllAndUniqueFailure(t *testing.T) {
	cwd := t.TempDir()
	p := filepath.Join(cwd, "b.txt")
	_ = os.WriteFile(p, []byte("x x x"), 0o644)
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
	_ = os.WriteFile(filepath.Join(cwd, "here.txt"), []byte("ok"), 0o644)
	res = b.Execute(context.Background(), map[string]any{"command": "cd / && cat here.txt"})
	if !res.IsError {
		// cat from / should fail; cwd reset next command
	}
	res = b.Execute(context.Background(), map[string]any{"command": "cat here.txt"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "ok") {
		t.Fatalf("cwd should reset to session: %+v", res)
	}
}

func TestBashBackgroundAndRead(t *testing.T) {
	cwd := t.TempDir()
	jobs := NewJobStore()
	set := Set{CWD: cwd, Jobs: jobs}
	all := set.All()
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
		idx := strings.Index(text, "output_file: ")
		path := strings.TrimSpace(strings.Split(text[idx+len("output_file: "):], "\n")[0])
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
	r := readTool{cwd: cwd}
	nb := `{"cells":[{"cell_type":"code","source":["print(1)\n"],"outputs":[]}]}`
	_ = os.WriteFile(filepath.Join(cwd, "n.ipynb"), []byte(nb), 0o644)
	res := r.Execute(context.Background(), map[string]any{"file_path": "n.ipynb"})
	if !strings.Contains(res.Content[0].Text, "cell 0") {
		t.Fatalf("ipynb: %s", res.Content[0].Text)
	}
	_ = os.WriteFile(filepath.Join(cwd, "a.pdf"), []byte("%PDF-1.4\n(HelloPDF)\n"), 0o644)
	res = r.Execute(context.Background(), map[string]any{"file_path": "a.pdf", "pages": "1-2"})
	if !strings.Contains(res.Content[0].Text, "pages=1-2") {
		t.Fatalf("pdf pages: %s", res.Content[0].Text)
	}

	marker := "KI-PDF-MARKER-42"
	stream := "BT /F1 18 Tf 20 60 Td (" + marker + ") Tj ET\n"
	real := "%PDF-1.1\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"4 0 obj<</Length " + strconv.Itoa(len(stream)) + ">>stream\n" + stream + "endstream\nendobj\n"
	_ = os.WriteFile(filepath.Join(cwd, "real.pdf"), []byte(real), 0o644)
	res = r.Execute(context.Background(), map[string]any{"file_path": "real.pdf"})
	if !strings.Contains(res.Content[0].Text, marker) {
		t.Fatalf("real pdf extract: %s", res.Content[0].Text)
	}
}
