package command

import (
	"os"
	"path/filepath"
	"testing"

	"ki/internal/resources"
	"ki/internal/session"
)

func TestParse(t *testing.T) {
	if p := Parse("hello"); p.Kind != KindPrompt {
		t.Fatalf("%+v", p)
	}
	if p := Parse("/usr/bin/true"); p.Kind != KindPrompt {
		t.Fatalf("%+v", p)
	}
	if p := Parse("/compact extra"); p.Kind != KindBuiltin || p.Name != "compact" || p.Args != "extra" {
		t.Fatalf("%+v", p)
	}
	if p := Parse("/Reload"); p.Kind != KindBuiltin || p.Name != "reload" {
		t.Fatalf("%+v", p)
	}
	if p := Parse("/skill:docx foo"); p.Kind != KindSkill || p.Name != "docx" || p.Args != "foo" {
		t.Fatalf("%+v", p)
	}
	if p := Parse("/review staged"); p.Kind != KindUnknown || p.Name != "review" {
		t.Fatalf("%+v", p)
	}
	if !AllowBusy(Parse("/reload")) || AllowBusy(Parse("/compact")) {
		t.Fatal("allow busy")
	}
}

func TestTemplatesProjectOverridesAndExpand(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".ki", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, "prompts", "review.md"), []byte("---\ndescription: global\n---\nglobal $1\n"), 0o600)
	_ = os.WriteFile(filepath.Join(cwd, ".ki", "prompts", "review.md"), []byte("---\ndescription: project\nargument-hint: [diff]\n---\nproject $1 $@\n"), 0o600)
	_ = os.WriteFile(filepath.Join(home, "prompts", "compact.md"), []byte("should not list\n"), 0o600)
	snapshot := resources.NewLoader(home).Scan(cwd)
	got, ok := ExpandTemplate(snapshot, "review", "a b")
	if !ok || got != "project a a b\n" {
		t.Fatalf("expand %q %v", got, ok)
	}
	items := Catalog(snapshot, session.Toggle{})
	var review Item
	for _, it := range items {
		if it.Name == "review" {
			review = it
		}
		if it.Name == "compact" && it.Source == "prompt" {
			t.Fatal("builtin name leaked as template")
		}
	}
	if review.Description != "project" || review.ArgumentHint != "[diff]" {
		t.Fatalf("%+v", review)
	}
	p := ResolveUnknown(Parse("/review"), snapshot)
	if p.Kind != KindTemplate {
		t.Fatalf("%+v", p)
	}
}
