package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ki/internal/session"
)

func TestPromptCreatesSessionAndPrintsFake(t *testing.T) {
	home, proj := isolate(t)
	out, errOut, code := runKI(t, "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("expected fake model text, got:\n%s", out)
	}
	id := mustSessionID(t, out, errOut)
	dir := sessionDir(t, home, id)
	raw := readJSONL(t, dir)
	if !strings.Contains(raw, `"type":"session"`) {
		t.Fatalf("missing header:\n%s", raw)
	}
	if !strings.Contains(raw, `"role":"user"`) || !strings.Contains(raw, `"role":"assistant"`) {
		t.Fatalf("missing messages:\n%s", raw)
	}
	cfg := readSessionConfig(t, dir)
	if cfg["model"] == "" {
		t.Fatalf("config: %+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRequiresSessionFlag(t *testing.T) {
	home, proj := isolate(t)
	out, errOut, code := runKI(t, "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("first: %d %s %s", code, out, errOut)
	}
	id1 := mustSessionID(t, out, errOut)

	out, errOut, code = runKI(t, "--session", id1, "again")
	if code != 0 {
		t.Fatalf("resume: %d %s %s", code, out, errOut)
	}
	raw := readJSONL(t, sessionDir(t, home, id1))
	if strings.Count(raw, `"role":"user"`) < 2 || strings.Count(raw, `"role":"assistant"`) < 2 {
		t.Fatalf("expected two turns:\n%s", raw)
	}

	out, errOut, code = runKI(t, "--cwd", proj, "third")
	if code != 0 {
		t.Fatalf("new: %d %s %s", code, out, errOut)
	}
	id2 := mustSessionID(t, out, errOut)
	if id2 == id1 {
		t.Fatal("missing --session must create a new session")
	}

	_, errOut, code = runKI(t, "--session", "ffffffffffffffffffffffffffffffff", "nope")
	if code == 0 {
		t.Fatalf("missing session should fail, stderr=%s", errOut)
	}
}

func TestCWDEncodesSessionPath(t *testing.T) {
	home, proj := isolate(t)
	out, errOut, code := runKI(t, "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("exit %d %s %s", code, out, errOut)
	}
	id := mustSessionID(t, out, errOut)
	dir := sessionDir(t, home, id)
	abs, _ := filepath.Abs(proj)
	if !strings.Contains(dir, session.EncodeCWD(abs)) {
		t.Fatalf("dir %s should contain encoded %s", dir, abs)
	}
	raw := readJSONL(t, dir)
	var hdr map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(raw, "\n", 2)[0]), &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr["cwd"] != abs {
		t.Fatalf("header cwd=%v want %s", hdr["cwd"], abs)
	}
}

func TestModelWritebackIsSessionOnly(t *testing.T) {
	home, proj := isolate(t)
	toml := filepath.Join(home, "ki.toml")
	_ = os.WriteFile(toml, []byte("[defaults]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-5\"\n"), 0o600)

	out, errOut, code := runKI(t, "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("first: %d %s %s", code, out, errOut)
	}
	id := mustSessionID(t, out, errOut)
	cfg := readSessionConfig(t, sessionDir(t, home, id))
	if cfg["model"] == "gpt-4o" {
		t.Fatalf("first model already gpt-4o: %+v", cfg)
	}

	out, errOut, code = runKI(t, "--session", id, "--model", "openai/gpt-4o", "switch")
	if code != 0 {
		t.Fatalf("switch: %d %s %s", code, out, errOut)
	}
	cfg = readSessionConfig(t, sessionDir(t, home, id))
	if cfg["provider"] != "openai" || cfg["model"] != "gpt-4o" {
		t.Fatalf("writeback: %+v", cfg)
	}
	raw, _ := os.ReadFile(toml)
	if strings.Contains(string(raw), "gpt-4o") {
		t.Fatalf("toml should be unchanged:\n%s", raw)
	}
}

func TestForkCopiesHistory(t *testing.T) {
	home, proj := isolate(t)
	out, errOut, code := runKI(t, "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("first: %d %s %s", code, out, errOut)
	}
	id := mustSessionID(t, out, errOut)

	out, errOut, code = runKI(t, "--session", id, "fork")
	if code != 0 {
		t.Fatalf("fork: %d %s %s", code, out, errOut)
	}
	id2 := strings.TrimSpace(out)
	if id2 == "" || id2 == id {
		t.Fatalf("fork id %q from %q", id2, out)
	}
	dir2 := sessionDir(t, home, id2)
	raw := readJSONL(t, dir2)
	if !strings.Contains(raw, `"role":"user"`) {
		t.Fatalf("fork missing history:\n%s", raw)
	}
	var hdr map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(raw, "\n", 2)[0]), &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr["parentSession"] == nil || hdr["parentSession"] == "" {
		t.Fatalf("parentSession: %+v", hdr)
	}

	out, errOut, code = runKI(t, "--session", id2, "only-child")
	if code != 0 {
		t.Fatalf("child prompt: %d %s %s", code, out, errOut)
	}
	if strings.Contains(readJSONL(t, sessionDir(t, home, id)), "only-child") {
		t.Fatal("parent jsonl must stay untouched")
	}
	if !strings.Contains(readJSONL(t, dir2), "only-child") {
		t.Fatal("child jsonl missing new prompt")
	}
}

func TestCompactCLI(t *testing.T) {
	home, proj := isolate(t)
	out, errOut, code := runKI(t, "--cwd", proj, "hello")
	if code != 0 {
		t.Fatalf("first: %d %s %s", code, out, errOut)
	}
	id := mustSessionID(t, out, errOut)
	out, errOut, code = runKI(t, "--session", id, "compact")
	if code != 0 {
		t.Fatalf("compact: %d %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "compacted") {
		t.Fatalf("compact stdout: %s", out)
	}
	_ = home
}

func TestCreateWithoutPrompt(t *testing.T) {
	home, proj := isolate(t)
	out, errOut, code := runKI(t, "--cwd", proj)
	if code != 0 {
		t.Fatalf("exit %d %s %s", code, out, errOut)
	}
	id := mustSessionID(t, out, errOut)
	raw := readJSONL(t, sessionDir(t, home, id))
	if !strings.Contains(raw, `"type":"session"`) {
		t.Fatalf("header:\n%s", raw)
	}
	if strings.Contains(raw, `"role":"user"`) {
		t.Fatalf("should not persist a user message:\n%s", raw)
	}
}
