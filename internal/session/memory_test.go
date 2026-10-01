package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"ki/internal/types"
)

func TestBodyCacheRejectsOversizedReadsAndShrinksTail(t *testing.T) {
	s, dir := seedTailSession(t, 0)
	t.Cleanup(func() { DropEntriesCache(dir) })
	if err := s.SeedTranscript(SeedSpec{Turns: 40, AssistantBytes: 256 << 10, RepeatSamePrompt: true}); err != nil {
		t.Fatal(err)
	}
	all, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(s.Entries()) || entriesCaches.items[filepath.Clean(dir)] != nil {
		t.Fatal("oversized read was incomplete or admitted")
	}
	tail, complete, err := TailEntries(dir, 5)
	if err != nil || complete || len(tail) >= len(all) {
		t.Fatalf("bounded tail: complete=%v entries=%d err=%v", complete, len(tail), err)
	}
	if all[0].ID != s.Entries()[0].ID {
		t.Fatal("tail read mutated an earlier reader's snapshot")
	}
	_, small := seedBigSession(t)
	t.Cleanup(func() { DropEntriesCache(small) })
	old, err := AllEntries(small)
	if err != nil {
		t.Fatal(err)
	}
	recent, complete, err := TailEntries(small, 5)
	if err != nil || complete || len(recent) >= len(old) {
		t.Fatalf("full promotion pinned tail: complete=%v err=%v", complete, err)
	}
}

func TestBodyCacheRevalidatesAtomicReplacement(t *testing.T) {
	s, dir := seedTailSession(t, 1)
	t.Cleanup(func() { DropEntriesCache(dir) })
	old, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := entriesPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := strings.Replace(string(raw), "turn 0", "turn X", 1)
	if err := os.WriteFile(path+".new", []byte(replacement), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path+".new", time.Now(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	updated, err := AllEntries(dir)
	if err != nil || updated[0].Message.Text() != "turn X" || old[0].Message.Text() != "turn 0" {
		t.Fatalf("replacement reused stale storage: %v", err)
	}
	_ = s.Close()
}

func TestWeightedCacheBudgetsAndStaleReaders(t *testing.T) {
	c := newWeightedCache[*int](100, 60)
	get := func(key string) *int { return c.get(key, func() *int { return new(int) }) }
	a, b := get("a"), get("b")
	c.update("a", a, 60)
	c.update("b", b, 40)
	if get("a") != a {
		t.Fatal("lost warm entry")
	}
	d := get("d")
	c.update("d", d, 30)
	if c.items["b"] != nil || c.bytes != 90 {
		t.Fatalf("LRU/budget: bytes=%d items=%v", c.bytes, c.items)
	}
	c.update("a", a, 61)
	if c.items["a"] != nil {
		t.Fatal("oversized value admitted")
	}
	replacement := get("a")
	c.update("a", replacement, 10)
	c.update("a", a, 50)
	if c.bytes != 40 || get("a") != replacement {
		t.Fatal("stale reader replaced a newer cache entry")
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				key := fmt.Sprintf("%d-%d", i, j)
				v := get(key)
				c.update(key, v, 30)
			}
		})
	}
	wg.Wait()
	if c.bytes > c.limit || len(c.items) > maxCachedSessions {
		t.Fatal("concurrent cache escaped budget")
	}
}

func TestDecodedPromptsShareStorageAcrossAppend(t *testing.T) {
	s, dir := seedTailSession(t, 0)
	tools := []ToolSchema{{Name: "apply_patch", Parameters: map[string]any{"type": "object", "properties": map[string]any{"patch": map[string]any{"type": "string"}}}, Format: &ToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: /.+/"}}}
	system := strings.Repeat("shared system ", 100)
	for range 3 {
		if _, err := s.AppendRequestHeader(system, tools); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertShared := func(entries []Entry) {
		t.Helper()
		for _, e := range entries[1:] {
			if unsafe.StringData(e.System) != unsafe.StringData(entries[0].System) || &e.Tools[0] != &entries[0].Tools[0] {
				t.Fatal("identical prompts allocate independent storage")
			}
		}
		if !reflect.DeepEqual(entries[0].Tools, tools) {
			t.Fatal("schema/grammar changed during interning")
		}
	}
	assertShared(entries)
	if _, err := s.AppendRequestHeader(system, tools); err != nil {
		t.Fatal(err)
	}
	entries, err = AllEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertShared(entries)
	DropEntriesCache(dir)
	if entriesCaches.items[filepath.Clean(dir)] != nil {
		t.Fatal("deleted cache retained prompt pool")
	}
}

func equalJSON(t *testing.T, want, got any) {
	t.Helper()
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(w) != string(g) {
		t.Fatalf("projection changed\nwant %s\ngot  %s", w, g)
	}
}

func TestMetadataPreviewKeepsUnicodeAndWhitespaceBoundaries(t *testing.T) {
	for _, s := range []string{"", " \t\n", " a\t\n b ", strings.Repeat("中🙂\u2003word\n", 200), strings.Repeat("a", 159) + " \t\n 中文 tail", strings.Repeat("a", 163) + " 中文 tail"} {
		want := utf8Prefix(strings.Join(strings.Fields(s), " "), 164)
		if got := metadataPreview(s); got != want {
			t.Fatalf("preview changed: want %q got %q", want, got)
		}
	}
}

func TestIndexedTranscriptMatchesFullProjections(t *testing.T) {
	s, dir := seedBigSession(t)
	if err := s.SetLeaf(s.Entries()[20].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "sibling branch"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: strings.Repeat("中文 🙂 hidden reply ", 300)}, {Type: "toolCall", ID: "call", Name: "Bash", Arguments: map[string]any{"command": "echo secret"}}}, Usage: &types.Usage{Input: 40000, Output: 50, CacheRead: 20000}, LatencyMs: 200, TTFTMs: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "toolResult", ToolCallID: "call", ToolName: "Bash", IsError: true, Content: []types.Content{{Type: "text", Text: strings.Repeat("result ", 1000)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(types.Message{Role: "user", Origin: "agent:child", Content: []types.Content{{Type: "text", Text: "runtime reply"}}}); err != nil {
		t.Fatal(err)
	}
	start, err := s.AppendDetailsEvent("compaction_start", map[string]any{"reason": "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendDetailsEvent("compaction_end", map[string]any{"status": "failed", "entryId": "", "paired": start.ID}); err != nil {
		t.Fatal(err)
	}
	all := s.Entries()
	leaf := s.LeafID()
	DropEntriesCache(dir)
	indexed, err := ReadTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, BuildIndex(all), indexed.Index())
	if entriesCaches.items[filepath.Clean(dir)] != nil {
		t.Fatal("index warmed full body cache")
	}
	for _, e := range indexed.Entries() {
		if e.System != "" || len(e.Tools) != 0 || len(e.RetainedTail) != 0 {
			t.Fatal("metadata pins prompt/retained bodies")
		}
	}
	for _, limit := range []int{1, 10, 100} {
		got, ok, err := indexed.Tail(leaf, "", "", limit)
		if err != nil || !ok {
			t.Fatalf("tail: %v", err)
		}
		equalJSON(t, BuildTail(all, leaf, limit, true), got)
		before := got.OldestID
		older, ok, err := indexed.Tail(leaf, before, "", limit)
		if err != nil || !ok {
			t.Fatalf("page: %v", err)
		}
		want := BuildBefore(all, leaf, before, limit)
		equalJSON(t, Tail{Entries: want.Entries, HasMore: want.HasMore, OldestID: want.OldestID}, older)
	}
	for _, keep := range []int{0, 1, 3, 20} {
		got, ok, err := indexed.Compact(leaf, "", "", keep)
		if err != nil || !ok {
			t.Fatalf("compact: %v", err)
		}
		equalJSON(t, BuildCompact(all, leaf, "", keep), got)
		for _, turn := range got.Turns {
			one, ok, err := indexed.Compact(leaf, "", turn.ID, keep)
			if err != nil || !ok {
				t.Fatalf("compact turn: %v", err)
			}
			want, _ := BuildCompactTurn(all, leaf, turn.ID, keep)
			equalJSON(t, want, one)
			detailed, ok, err := indexed.Tail(leaf, "", turn.ID, 10)
			if err != nil || !ok {
				t.Fatalf("detailed turn: %v", err)
			}
			wt, _ := BuildTurn(all, leaf, turn.ID, "", 10)
			equalJSON(t, wt, detailed)
		}
	}
	ids := []string{all[0].ID, leaf, all[0].ID, "missing"}
	bodies, err := indexed.Lookup(ids)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, LookupEntries(all, ids), bodies)
	if _, ok, err := indexed.Tail(leaf, "missing", "", 10); err != nil || ok {
		t.Fatal("missing cursor accepted")
	}
}

func TestTranscriptAppendRewriteAndTornRecords(t *testing.T) {
	s, dir := seedTailSession(t, 2)
	first, err := ReadTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldJSON, _ := json.Marshal(first.Index())
	if _, err := s.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "append"}}}); err != nil {
		t.Fatal(err)
	}
	second, err := ReadTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, BuildIndex(s.Entries()), second.Index())
	unchanged, _ := json.Marshal(first.Index())
	if string(oldJSON) != string(unchanged) {
		t.Fatal("append mutated an in-flight snapshot")
	}
	if _, err := first.Lookup([]string{first.entries[0].ID}); err != nil {
		t.Fatalf("append invalidated old offsets: %v", err)
	}
	f, err := os.OpenFile(entriesPath(dir), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":"torn`)
	_ = f.Close()
	torn, err := ReadTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, second.Index(), torn.Index())
	f, err = os.OpenFile(entriesPath(dir), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`","type":"custom","summary":"finished"}` + "\n")
	_ = f.Close()
	completed, err := ReadTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	if completed.index[len(completed.index)-1].ID != "torn" {
		t.Fatal("torn offset skipped")
	}
	_, freshDir := seedTailSession(t, 1)
	if err := os.WriteFile(entriesPath(dir), readFile(t, entriesPath(freshDir)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := completed.Lookup([]string{completed.entries[0].ID}); err == nil {
		t.Fatal("stale offsets read rewritten file")
	}
	rewritten, err := ReadTranscript(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.entries[0].ID == completed.entries[0].ID {
		t.Fatal("rewrite retained old metadata")
	}
}
