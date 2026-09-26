package tooloutput

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ki/internal/types"
)

func newTestStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	if cfg.Root == "" {
		cfg.Root = t.TempDir()
	}
	store, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func textBlock(text string) []types.Content {
	return []types.Content{{Type: "text", Text: text}}
}

func TestNormalizeSpillsCompleteTextAndReturnsPreview(t *testing.T) {
	store := newTestStore(t, Config{PreviewBytes: 32, PreviewLines: 2})
	full := "line-1\nline-2\nline-3\n" + strings.Repeat("x", 40)
	content, ref := store.Normalize("session-1", "Grep", nil, textBlock(full), nil)
	if ref == nil || !ref.Truncated || ref.Incomplete {
		t.Fatalf("reference = %+v, want complete truncated reference", ref)
	}
	if len(content) != 1 || !strings.Contains(content[0].Text, "Stored:") || !strings.Contains(content[0].Text, "Use Read") {
		t.Fatalf("content = %+v", content)
	}
	raw, err := os.ReadFile(ref.Path) //nolint:gosec // path is the test store output
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != full {
		t.Fatalf("spilled output changed: %q", raw)
	}
	if !filepath.IsAbs(ref.Path) || ref.TotalBytes != len(full) || ref.Bytes != len(full) {
		t.Fatalf("reference = %+v", ref)
	}
}

func TestNormalizeLeavesSmallTextUntouched(t *testing.T) {
	store := newTestStore(t, Config{})
	want := textBlock("small")
	got, ref := store.Normalize("session-1", "Read", nil, want, nil)
	if ref != nil || len(got) != 1 || got[0].Text != "small" {
		t.Fatalf("got=%+v ref=%+v", got, ref)
	}
}

func TestNormalizeKeepsNonTextBlocks(t *testing.T) {
	store := newTestStore(t, Config{PreviewBytes: 8, PreviewLines: 1})
	content := []types.Content{
		{Type: "text", Text: strings.Repeat("x", 64)},
		{Type: "image", Data: "AAAA", MIMEType: "image/png"},
	}
	got, ref := store.Normalize("session-1", "Read", nil, content, nil)
	if ref == nil {
		t.Fatal("expected a spill reference")
	}
	if len(got) != 2 || got[1].Type != "image" || got[1].Data != "AAAA" {
		t.Fatalf("non-text block was dropped: %+v", got)
	}
}

func TestNormalizeSkipsResultsThatAlreadyCarryAReference(t *testing.T) {
	store := newTestStore(t, Config{PreviewBytes: 8, PreviewLines: 1})
	big := textBlock(strings.Repeat("x", 4096))
	taskDetails := struct {
		OutputFile string `json:"output_file"`
	}{OutputFile: filepath.Join(t.TempDir(), "existing.txt")}
	if _, ref := store.Normalize("session-1", "Bash", nil, big, taskDetails); ref != nil {
		t.Fatalf("shell result was respilled: %+v", ref)
	}
	readDetails := map[string]any{"truncation": map[string]any{"next_offset": float64(2001)}}
	if _, ref := store.Normalize("session-1", "Read", nil, big, readDetails); ref != nil {
		t.Fatalf("paged Read result was respilled: %+v", ref)
	}
}

// Reading a spill file through Read must not create a second copy of it; the
// model would otherwise chase a reference that points at its own page.
func TestNormalizeSkipsReadOfOwnSpillFile(t *testing.T) {
	store := newTestStore(t, Config{PreviewBytes: 8, PreviewLines: 1})
	_, ref := store.Normalize("session-1", "Grep", nil, textBlock(strings.Repeat("y", 128)), nil)
	if ref == nil {
		t.Fatal("expected a spill reference")
	}
	if !store.Owns(ref.Path) {
		t.Fatalf("store does not own its own file: %s", ref.Path)
	}
	content, second := store.Normalize("session-1", "Read", map[string]any{"file_path": ref.Path}, textBlock(strings.Repeat("y", 128)), nil)
	if second != nil || content[0].Text != strings.Repeat("y", 128) {
		t.Fatalf("re-spilled a spill file: ref=%+v content=%+v", second, content)
	}
}

func TestNormalizeCapsOneFileAndMarksItIncomplete(t *testing.T) {
	store := newTestStore(t, Config{PreviewBytes: 4, PreviewLines: 1, MaxFileBytes: 10})
	full := strings.Repeat("z", 100)
	content, ref := store.Normalize("session-1", "Grep", nil, textBlock(full), nil)
	if ref == nil || !ref.Incomplete || ref.Bytes != 10 || ref.TotalBytes != 100 {
		t.Fatalf("reference = %+v", ref)
	}
	raw, err := os.ReadFile(ref.Path) //nolint:gosec // path is the test store output
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 10 {
		t.Fatalf("stored %d bytes, want the 10 byte file limit", len(raw))
	}
	if !strings.Contains(content[0].Text, "single-file limit") {
		t.Fatalf("content does not explain the file limit: %s", content[0].Text)
	}
}

func TestNormalizeStopsSpillingWhenTheSessionBudgetIsGone(t *testing.T) {
	store := newTestStore(t, Config{PreviewBytes: 4, PreviewLines: 1, MaxSessionBytes: 12})
	full := strings.Repeat("q", 10)
	if _, ref := store.Normalize("session-1", "Grep", nil, textBlock(full), nil); ref == nil {
		t.Fatal("first result should be stored")
	}
	content, ref := store.Normalize("session-1", "Grep", nil, textBlock(full), nil)
	if ref != nil {
		t.Fatalf("second result exceeded the budget: %+v", ref)
	}
	if !strings.Contains(content[0].Text, "budget") {
		t.Fatalf("content does not explain the budget: %s", content[0].Text)
	}
	if body, _, _ := strings.Cut(content[0].Text, "\n\n["); len(body) > 4 {
		t.Fatalf("preview is not bounded: %q", body)
	}
	// A different session has its own budget.
	if _, ref := store.Normalize("session-2", "Grep", nil, textBlock(full), nil); ref == nil {
		t.Fatal("a second session should still be able to spill")
	}
}

func TestMergeDetailsKeepsToolFieldsFlatAndSetsOutput(t *testing.T) {
	ref := &Reference{Path: "/tmp/x", Bytes: 4, TotalBytes: 9, Truncated: true}
	merged := MergeDetails(map[string]any{"matches": float64(3), "truncated": true}, ref)
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["matches"] != float64(3) {
		t.Fatalf("tool details were nested or lost: %v", got)
	}
	output, ok := got["output"].(map[string]any)
	if !ok || output["path"] != "/tmp/x" {
		t.Fatalf("output reference missing: %v", got)
	}
	if MergeDetails(nil, nil) != nil {
		t.Fatal("nil reference must not change details")
	}
	nilDetails, ok := MergeDetails(nil, ref).(map[string]any)
	if !ok || nilDetails["output"] != ref {
		t.Fatalf("details = %v", MergeDetails(nil, ref))
	}
}

func TestCreateOutputFileLivesInsideTheSessionDirectory(t *testing.T) {
	store := newTestStore(t, Config{})
	file, err := store.CreateOutputFile("session-1", "bash")
	if err != nil {
		t.Fatal(err)
	}
	if !store.Owns(file.Name()) {
		t.Fatalf("task output %s is not owned by the store", file.Name())
	}
	if _, err := file.WriteString("hi"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseSession("session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file.Name()); !os.IsNotExist(err) {
		t.Fatalf("session close left the task output behind: %v", err)
	}
}

func TestCloseSessionRemovesSpillFiles(t *testing.T) {
	store := newTestStore(t, Config{PreviewBytes: 4, PreviewLines: 1})
	_, ref := store.Normalize("session-1", "Grep", nil, textBlock(strings.Repeat("x", 64)), nil)
	if err := store.CloseSession("session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ref.Path); !os.IsNotExist(err) {
		t.Fatalf("spill file still exists: %v", err)
	}
}

func TestSweepRemovesDeadOwnersAndKeepsLiveOnes(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	// A PID above every pid_max is safely not running.
	dead := filepath.Join(base, runDirPrefix+"dead")
	mkdirOwner(t, dead, ownerInfo{PID: 1 << 30, Host: testHost(t), StartedAt: now.Add(-time.Hour), Heartbeat: now.Add(-time.Hour)})
	live := filepath.Join(base, runDirPrefix+fmt.Sprint(os.Getpid())+"-live")
	mkdirOwner(t, live, ownerInfo{PID: os.Getpid(), Host: testHost(t), StartedAt: now, Heartbeat: now})
	foreign := filepath.Join(base, "not-a-run-root")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(base, runDirPrefix+"unknown")
	mkdirOwner(t, unknown, ownerInfo{PID: 4242, Host: "another-host", StartedAt: now, Heartbeat: now})

	newTestStore(t, Config{Root: base, TTL: time.Hour, Now: func() time.Time { return now }})

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead owner was not swept: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live owner was swept: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("unrelated directory was swept: %v", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("fresh foreign-host root was swept: %v", err)
	}
}

func TestSweepRemovesStaleForeignRoots(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	stale := filepath.Join(base, runDirPrefix+"stale")
	mkdirOwner(t, stale, ownerInfo{PID: 4242, Host: "another-host", StartedAt: now.Add(-48 * time.Hour), Heartbeat: now.Add(-48 * time.Hour)})
	noOwner := filepath.Join(base, runDirPrefix+"no-owner")
	if err := os.MkdirAll(noOwner, 0o700); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(noOwner, old, old); err != nil {
		t.Fatal(err)
	}
	newTestStore(t, Config{Root: base, TTL: time.Hour, Now: func() time.Time { return now }})
	for _, path := range []string{stale, noOwner} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s was not swept: %v", path, err)
		}
	}
}

func TestCloseRemovesTheRunRoot(t *testing.T) {
	store := newTestStore(t, Config{})
	root := store.Root()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("run root survived Close: %v", err)
	}
	if store.Owns(filepath.Join(root, "x")) {
		t.Fatal("a closed store still owns paths")
	}
}

// mkdirOwner writes a run root whose timestamps match its recorded heartbeat,
// which is what a real root looks like: the last write refreshes both.
func mkdirOwner(t *testing.T, dir string, info ownerInfo) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	ownerPath := filepath.Join(dir, ownerFileName)
	if err := os.WriteFile(ownerPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if info.Heartbeat.IsZero() {
		return
	}
	if err := os.Chtimes(ownerPath, info.Heartbeat, info.Heartbeat); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, info.Heartbeat, info.Heartbeat); err != nil {
		t.Fatal(err)
	}
}

func testHost(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return host
}
