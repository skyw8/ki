package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepAndGlobUseEmbeddedRipgrep(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"src/main.go":    "package main\nfunc main() {}\n",
		"src/other.go":   "package main\nfunc helper() {}\n",
		"src/readme.txt": "func main is documented here\n",
		"ignored.go":     "func main() {}\n",
		".gitignore":     "ignored.go\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	grep, err := (Engine{}).Grep(context.Background(), GrepRequest{
		Pattern:       `func main`,
		Root:          root,
		OutputMode:    "content",
		MaxResults:    20,
		IncludeHidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(grep.Matches) != 2 {
		t.Fatalf("grep matches = %d, want 2: %+v", len(grep.Matches), grep.Matches)
	}
	for _, match := range grep.Matches {
		if strings.HasSuffix(match.Path, "ignored.go") {
			t.Fatalf("grep ignored .gitignore entry: %+v", match)
		}
	}

	glob, err := (Engine{}).Glob(context.Background(), GlobRequest{
		Pattern:       "**/*.go",
		Root:          root,
		MaxResults:    10,
		NoIgnore:      true,
		IncludeHidden: true,
		SortModified:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(glob.Files) != 3 {
		t.Fatalf("glob files = %d, want 3: %v", len(glob.Files), glob.Files)
	}
}
