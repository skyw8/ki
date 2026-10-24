package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/tool/builtin/catalog"
)

func writeSideSearchFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfinedSideSearchModesAndContext(t *testing.T) {
	root := questionSideTempDir(t)
	writeSideSearchFile(t, root, "a.go", "before\nAlpha\nmiddle\nalpha\n")
	writeSideSearchFile(t, root, "b.txt", "Alpha\n")
	for _, test := range []struct {
		mode string
		want string
	}{
		{"content", "a.go:2:Alpha"},
		{"count", "a.go:2"},
		{"files_with_matches", "a.go"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			result := executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{
				"pattern": "alpha", "-i": true, "type": "go", "output_mode": test.mode, "context": 1,
			}, root, []string{root})
			if result.IsError || !strings.Contains(result.Content[0].Text, test.want) || strings.Contains(result.Content[0].Text, "b.txt") {
				t.Fatalf("search = %+v", result)
			}
			if test.mode == "content" && !strings.Contains(result.Content[0].Text, "a.go-1-before") {
				t.Fatalf("context missing: %+v", result)
			}
		})
	}
	result := executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{
		"pattern": "alpha", "-i": true, "output_mode": "content", "head_limit": 1, "offset": 1, "-n": false,
	}, root, []string{root})
	if result.IsError || !strings.Contains(result.Content[0].Text, "a.go:alpha") || strings.Contains(result.Content[0].Text, "a.go:Alpha") {
		t.Fatalf("pagination = %+v", result)
	}
}

func TestConfinedSideSearchMultilineAndValidation(t *testing.T) {
	root := questionSideTempDir(t)
	writeSideSearchFile(t, root, "a.txt", "first\nsecond\n")
	args := map[string]any{"pattern": `first\s+second`, "output_mode": "content"}
	result := executeQuestionSideSearch(context.Background(), catalog.Grep, args, root, []string{root})
	if result.IsError || !strings.Contains(result.Content[0].Text, "No matches found") {
		t.Fatalf("single line = %+v", result)
	}
	args["multiline"] = true
	result = executeQuestionSideSearch(context.Background(), catalog.Grep, args, root, []string{root})
	if result.IsError || !strings.Contains(result.Content[0].Text, "a.txt:1:first") || !strings.Contains(result.Content[0].Text, "a.txt:2:second") {
		t.Fatalf("multiline = %+v", result)
	}
	for _, invalid := range []map[string]any{
		{"pattern": "(?=first)"},
		{"pattern": "first", "offset": -1},
		{"pattern": "first", "head_limit": 0.5},
		{"pattern": "first", "type": "unknown-file-type"},
		{"pattern": "first", "output_mode": "invalid"},
	} {
		result := executeQuestionSideSearch(context.Background(), catalog.Grep, invalid, root, []string{root})
		if !result.IsError {
			t.Fatalf("invalid search accepted: %+v", invalid)
		}
	}
}

func TestConfinedSideGlobAndLocalIgnores(t *testing.T) {
	root := questionSideTempDir(t)
	writeSideSearchFile(t, root, ".gitignore", "*.log\nbuild/\n!kept.log\n")
	writeSideSearchFile(t, root, "src/a.go", "found")
	writeSideSearchFile(t, root, "root.go", "found")
	writeSideSearchFile(t, root, "ignored.log", "found")
	writeSideSearchFile(t, root, "kept.log", "found")
	writeSideSearchFile(t, root, "build/hidden.go", "found")
	writeSideSearchFile(t, root, "src/.ignore", "nested.txt\n")
	writeSideSearchFile(t, root, "src/nested.txt", "found")
	result := executeQuestionSideSearch(context.Background(), catalog.Glob, map[string]any{"pattern": "**/*.go"}, root, []string{root})
	if result.IsError || !strings.Contains(result.Content[0].Text, "root.go") || !strings.Contains(result.Content[0].Text, filepath.Join("src", "a.go")) || strings.Contains(result.Content[0].Text, "hidden.go") {
		t.Fatalf("glob = %+v", result)
	}
	result = executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{"pattern": "found"}, root, []string{root})
	if result.IsError || strings.Contains(result.Content[0].Text, "ignored.log") || strings.Contains(result.Content[0].Text, "nested.txt") || !strings.Contains(result.Content[0].Text, "kept.log") {
		t.Fatalf("ignores = %+v", result)
	}
	result = executeQuestionSideSearch(context.Background(), catalog.Glob, map[string]any{"pattern": "**/*.go", "respect_gitignore": false}, root, []string{root})
	if result.IsError || !strings.Contains(result.Content[0].Text, "hidden.go") {
		t.Fatalf("ignore override = %+v", result)
	}
	result = executeQuestionSideSearch(context.Background(), catalog.Glob, map[string]any{"pattern": "../*"}, root, []string{root})
	if !result.IsError {
		t.Fatal("parent glob traversal accepted")
	}
}

func TestConfinedSideSearchRejectsOutsideAndSymlinkTraversal(t *testing.T) {
	root, outside := questionSideTempDir(t), questionSideTempDir(t)
	writeSideSearchFile(t, root, "allowed.txt", "public needle")
	writeSideSearchFile(t, outside, "secret.txt", "private needle")
	result := executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{"pattern": "needle", "path": outside}, root, []string{root})
	if !result.IsError {
		t.Fatalf("outside search accepted: %+v", result)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result = executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{"pattern": "needle", "output_mode": "content"}, root, []string{root})
	if result.IsError || strings.Contains(result.Content[0].Text, "private") || !strings.Contains(result.Content[0].Text, "public") {
		t.Fatalf("symlink escaped: %+v", result)
	}
	result = executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{"pattern": "needle", "path": filepath.Join(root, "escape")}, root, []string{root})
	if !result.IsError {
		t.Fatalf("explicit symlink escaped: %+v", result)
	}
}

func TestConfinedSideSearchPinsRootAcrossRenameAndRejectsAncestorSwap(t *testing.T) {
	base := questionSideTempDir(t)
	root, outside := filepath.Join(base, "workspace"), questionSideTempDir(t)
	writeSideSearchFile(t, root, "data.txt", "public")
	writeSideSearchFile(t, outside, "data.txt", "private")
	pinned, _, allowed, err := openSideSearchPath(root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Skipf("open root rename unavailable: %v", err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	walk := sideSearchWalk{ctx: context.Background(), root: pinned, base: allowed}
	if err := walk.visit(".", nil); err != nil {
		t.Fatal(err)
	}
	result := executeSideGrep(context.Background(), map[string]any{"pattern": ".", "output_mode": "content"}, walk, root, root)
	if result.IsError || !strings.Contains(result.Content[0].Text, "public") || strings.Contains(result.Content[0].Text, "private") {
		t.Fatalf("root swap leaked content: %+v", result)
	}
	result = executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{"pattern": "."}, root, []string{root})
	if !result.IsError {
		t.Fatalf("changed root accepted: %+v", result)
	}
	if _, err := openPinnedSideRoot(filepath.Join(root, "nested")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

func TestConfinedSideSearchBudgetsAndCancellation(t *testing.T) {
	root := questionSideTempDir(t)
	writeSideSearchFile(t, root, "large.txt", strings.Repeat("x", sideSearchMaxFileBytes+1))
	result := executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{"pattern": "x", "output_mode": "content"}, root, []string{root})
	if result.IsError || !strings.Contains(result.Content[0].Text, "truncated") {
		t.Fatalf("large file budget = %+v", result)
	}
	writeSideSearchFile(t, root, "many.txt", strings.Repeat(strings.Repeat("x", 500)+"\n", 900))
	result = executeQuestionSideSearch(context.Background(), catalog.Grep, map[string]any{"pattern": "x", "output_mode": "content", "head_limit": 0}, root, []string{root})
	if result.IsError || len(result.Content[0].Text) > sideSearchMaxOutput+256 {
		t.Fatalf("output budget = %+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = executeQuestionSideSearch(ctx, catalog.Grep, map[string]any{"pattern": "x"}, root, []string{root})
	if !result.IsError {
		t.Fatal("canceled search succeeded")
	}
}

func TestConfinedSideSearchRejectsDescendantSwapAfterEnumeration(t *testing.T) {
	root, outside := questionSideTempDir(t), questionSideTempDir(t)
	writeSideSearchFile(t, root, filepath.Join("nested", "data.txt"), "public")
	writeSideSearchFile(t, outside, "data.txt", "private")
	pinned, _, allowed, err := openSideSearchPath(root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	walk := sideSearchWalk{ctx: context.Background(), root: pinned, base: allowed}
	if err := walk.visit(".", nil); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Rename(nested, filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, nested); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result := executeSideGrep(context.Background(), map[string]any{"pattern": ".", "output_mode": "content"}, walk, root, root)
	if result.IsError || strings.Contains(result.Content[0].Text, "private") {
		t.Fatalf("descendant race leaked content: %+v", result)
	}
}
