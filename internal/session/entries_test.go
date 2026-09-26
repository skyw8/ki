package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ki/internal/types"
)

func seedTailSession(t *testing.T, turns int) (*Session, string) {
	t.Helper()
	root := t.TempDir()
	s, err := Create(root, t.TempDir(), "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedTranscript(SeedSpec{Turns: turns, AssistantBytes: 4096, ToolResultBytes: 64, RepeatSamePrompt: true}); err != nil {
		t.Fatal(err)
	}
	return s, s.Dir
}

// seedBigSession writes a transcript larger than one tail read, so the tests
// exercise a real window instead of a file that fits in the first read.
func seedBigSession(t *testing.T) (*Session, string) {
	t.Helper()
	s, dir := seedTailSession(t, 400)
	info, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= tailReadBytes {
		t.Fatalf("fixture too small: %d bytes", info.Size())
	}
	return s, dir
}

func TestTailEntriesWindow(t *testing.T) {
	s, dir := seedBigSession(t)
	defer func() { _ = s.Close() }()
	total := len(s.Entries())
	if total < 100 {
		t.Fatalf("seed: %d entries", total)
	}

	entries, complete, err := TailEntries(dir, DefaultViewLimit)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("a window of the transcript must not report complete")
	}
	if len(entries) < DefaultViewLimit || len(entries) >= total {
		t.Fatalf("window = %d of %d entries", len(entries), total)
	}
	all, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != total {
		t.Fatalf("all = %d want %d", len(all), total)
	}
	// The window must be the tail of the file, in the same order.
	offset := len(all) - len(entries)
	for i, e := range entries {
		if e.ID != all[offset+i].ID {
			t.Fatalf("window[%d] = %s want %s", i, e.ID, all[offset+i].ID)
		}
	}
	// A request for more than the file holds returns everything.
	entries, complete, err = TailEntries(dir, total*2)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(entries) != total {
		t.Fatalf("oversized window: n=%d complete=%v", len(entries), complete)
	}
}

// The cache must follow an append-only file: entries written after a read are
// picked up without re-reading what was already decoded.
func TestTailEntriesFollowsAppend(t *testing.T) {
	s, dir := seedTailSession(t, 30)
	defer func() { _ = s.Close() }()
	before, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedTranscript(SeedSpec{Turns: 5, AssistantBytes: 8, RepeatSamePrompt: true}); err != nil {
		t.Fatal(err)
	}
	after, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+len(s.Entries())-len(before) {
		t.Fatalf("after = %d before = %d session = %d", len(after), len(before), len(s.Entries()))
	}
	if len(after) <= len(before) {
		t.Fatalf("append not picked up: %d", len(after))
	}
	for i, e := range before {
		if after[i].ID != e.ID {
			t.Fatalf("cached prefix changed at %d", i)
		}
	}
	// A tail read served after the full read keeps the same trailing order.
	tail, _, err := TailEntries(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range tail {
		if e.ID != after[len(after)-len(tail)+i].ID {
			t.Fatalf("tail[%d] = %s", i, e.ID)
		}
	}
}

// A file that shrank or was replaced must be read from scratch rather than
// extended from an offset that no longer describes it.
func TestTailEntriesResetsRewrittenFile(t *testing.T) {
	s, dir := seedTailSession(t, 20)
	defer func() { _ = s.Close() }()
	if _, err := AllEntries(dir); err != nil {
		t.Fatal(err)
	}
	// Replace the transcript with one short session: fewer bytes, same path.
	fresh, err := Create(filepath.Dir(filepath.Dir(dir)), t.TempDir(), "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	if err := fresh.SeedTranscript(SeedSpec{Turns: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), readFile(t, filepath.Join(fresh.Dir, "events.jsonl")), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(fresh.Entries()) {
		t.Fatalf("rewritten file: %d entries want %d", len(got), len(fresh.Entries()))
	}
	for i, e := range got {
		if e.ID != fresh.Entries()[i].ID {
			t.Fatalf("entry %d = %s want %s", i, e.ID, fresh.Entries()[i].ID)
		}
	}
}

// A half-written trailing line is left for the next read instead of failing.
func TestTailEntriesSkipsTornLine(t *testing.T) {
	s, dir := seedTailSession(t, 3)
	defer func() { _ = s.Close() }()
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","id":"torn`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got, err := AllEntries(dir)
	if err != nil {
		t.Fatalf("torn line must not fail the read: %v", err)
	}
	if len(got) != len(s.Entries()) {
		t.Fatalf("torn read = %d want %d", len(got), len(s.Entries()))
	}
}

// The window of a leaf that sits on an older branch grows until the chain is
// covered, instead of returning a window the branch does not reach.
func TestLeafTailCoversOlderBranch(t *testing.T) {
	s, dir := seedBigSession(t)
	defer func() { _ = s.Close() }()
	all, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Re-point the leaf at an early entry, as an edit/regenerate would.
	old := all[2].ID
	if err := s.SetLeaf(old); err != nil {
		t.Fatal(err)
	}
	window, _, err := LeafTail(dir, old, DefaultViewLimit)
	if err != nil {
		t.Fatal(err)
	}
	chain := LeafChain(window, old)
	want := LeafChain(all, old)
	if len(chain) != len(want) || len(chain) == 0 || chain[len(chain)-1].ID != old {
		t.Fatalf("chain = %d (want %d), last = %s", len(chain), len(want), chain[len(chain)-1].ID)
	}
	if len(window) == 0 {
		t.Fatal("empty window")
	}
}

func TestLeafChainExcludesOtherBranches(t *testing.T) {
	s, dir := seedTailSession(t, 3)
	defer func() { _ = s.Close() }()
	all, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := all[0].ID
	if err := s.SetLeaf(first); err != nil {
		t.Fatal(err)
	}
	chain := LeafChain(all, first)
	if len(chain) != 1 || chain[0].ID != first {
		t.Fatalf("chain = %+v", chain)
	}
}

func TestOpenFromMatchesOpen(t *testing.T) {
	s, dir := seedTailSession(t, 5)
	defer func() { _ = s.Close() }()
	header, err := ReadHeader(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	from := OpenFrom(dir, header, cfg, entries)
	opened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if from.ID() != opened.ID() || from.LeafID() != opened.LeafID() {
		t.Fatalf("id %s/%s leaf %s/%s", from.ID(), opened.ID(), from.LeafID(), opened.LeafID())
	}
	if len(from.Entries()) != len(opened.Entries()) {
		t.Fatalf("entries %d/%d", len(from.Entries()), len(opened.Entries()))
	}
	// The handle must not alias the shared cache: appending goes to the file and
	// to its own slice only.
	before := len(entries)
	if _, err := from.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "after OpenFrom"}}}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != before {
		t.Fatal("AppendMessage wrote into the cached slice")
	}
	if len(from.Entries()) != before+1 {
		t.Fatalf("appended entries = %d want %d", len(from.Entries()), before+1)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestReadConfigWaitsForTheFileGate pins that ReadConfig shares the session file
// gate with writeConfig. Windows refuses an open that lands inside the temp-file
// + rename replace ("The process cannot access the file because it is being used
// by another process"), so the gate — not luck — is what makes a config read
// safe while a run appends. Without it the Windows e2e run saw spurious 404s
// from POST /v1/sessions/{id}/prompt.
func TestReadConfigWaitsForTheFileGate(t *testing.T) {
	s, err := Create(t.TempDir(), t.TempDir(), "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	gate := fileGate(s.Dir)
	gate.Lock()

	type result struct {
		cfg Config
		err error
	}
	done := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		cfg, readErr := ReadConfig(s.Dir)
		done <- result{cfg: cfg, err: readErr}
	}()
	<-started
	select {
	case got := <-done:
		gate.Unlock()
		t.Fatalf("ReadConfig returned while the writer held the gate: %+v %v", got.cfg, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	gate.Unlock()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.cfg.Model != "model" {
			t.Fatalf("config = %+v", got.cfg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadConfig did not finish after the gate was released")
	}
}
