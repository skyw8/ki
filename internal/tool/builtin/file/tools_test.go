package filetools

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	toolapi "ki/internal/tool"
)

func pick(ts []toolapi.Tool, name string) toolapi.Tool {
	for _, t := range ts {
		if toolapi.Equal(name, t.Name()) {
			return t
		}
	}
	return nil
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
	all := set.Build(true, false)
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
	set := Set{CWD: cwd}.Build(false, false)
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
