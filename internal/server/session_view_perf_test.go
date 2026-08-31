package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ki/internal/session"
)

func timedSessionGET(t *testing.T, hs *httptest.Server, path string) (time.Duration, []byte, map[string]any) {
	t.Helper()
	start := time.Now()
	res, err := authedGet(t, hs, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s %d %s", path, res.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return elapsed, body, got
}

func TestSessionViewPerf(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	dir, ok := srv.sidx.Lookup(id)
	if !ok {
		t.Fatal("session dir")
	}
	sess, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SeedTranscript(session.SeedSpec{
		Turns:            80,
		AssistantBytes:   64,
		ToolResultBytes:  8 * 1024,
		SystemBytes:      4096,
		Title:            "perf-history",
		RepeatSamePrompt: true,
	}); err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()
	jsonl, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	first, raw, got := timedSessionGET(t, hs, "/v1/sessions/"+id)
	if _, ok := got["messages"]; ok {
		t.Fatal("GET still includes messages")
	}
	index, _ := got["index"].([]any)
	entries, _ := got["entries"].([]any)
	if len(index) < 80 || len(entries) == 0 {
		t.Fatalf("index=%d entries=%d", len(index), len(entries))
	}
	if got["hasMore"] != true {
		t.Fatal("expected hasMore")
	}
	if unchanged := countFlag(entries, "promptUnchanged"); unchanged == 0 {
		t.Fatal("repeated request headers should omit system/tools")
	}
	oldest, _ := got["oldestId"].(string)
	if oldest == "" {
		t.Fatal("oldestId")
	}
	second, _, _ := timedSessionGET(t, hs, "/v1/sessions/"+id)
	runtimeDur, runtimeBody, runtime := timedSessionGET(t, hs, "/v1/sessions/"+id+"?fields=runtime")
	if _, ok := runtime["entries"]; ok {
		t.Fatal("fields=runtime leaked entries")
	}
	beforeDur, beforeBody, beforeGot := timedSessionGET(t, hs, "/v1/sessions/"+id+"?before="+oldest)
	beforeEntries, _ := beforeGot["entries"].([]any)
	if len(beforeEntries) == 0 {
		t.Fatal("before window empty")
	}
	if _, ok := beforeGot["index"]; ok {
		t.Fatal("before must omit index")
	}

	hugeID := createSession(t, hs, t.TempDir())
	hugeDir, ok := srv.sidx.Lookup(hugeID)
	if !ok {
		t.Fatal("huge dir")
	}
	huge, err := session.Open(hugeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := huge.SeedTranscript(session.SeedSpec{Turns: 1, AssistantBytes: 300 * 1024, Title: "perf-huge"}); err != nil {
		t.Fatal(err)
	}
	leaf := huge.LeafID()
	_ = huge.Close()
	hugeJSONL, err := os.Stat(filepath.Join(hugeDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hugeDur, hugeBody, hugeGot := timedSessionGET(t, hs, "/v1/sessions/"+hugeID)
	ents, _ := hugeGot["entries"].([]any)
	truncated := false
	for _, item := range ents {
		m, _ := item.(map[string]any)
		if m["truncated"] == true {
			truncated = true
		}
	}
	if !truncated {
		t.Fatal("huge assistant missing truncated")
	}
	entryDur, entryBody, entryGot := timedSessionGET(t, hs, "/v1/sessions/"+hugeID+"?entry="+leaf)
	entry, _ := entryGot["entry"].(map[string]any)
	if entry["truncated"] == true {
		t.Fatal("?entry must return the full row")
	}

	t.Logf("\n%-18s %10s %10s  %s\n%-18s %10d %10s  %s\n%-18s %10d %10s  %s\n%-18s %10d %10s  %s\n%-18s %10d %10s  %s\n%-18s %10d %10s  %s\n%-18s %10d %10s  %s\n%-18s %10d %10s  %s\n%-18s %10d %10s  %s",
		"case", "bytes", "ms", "note",
		"history jsonl", jsonl.Size(), "-", "disk",
		"history GET", len(raw), first.Round(time.Millisecond), "slim view",
		"history GET#2", len(raw), second.Round(time.Millisecond), "mtime cache",
		"fields=runtime", len(runtimeBody), runtimeDur.Round(time.Millisecond), "no transcript",
		"history before", len(beforeBody), beforeDur.Round(time.Millisecond), "older leaf",
		"huge jsonl", hugeJSONL.Size(), "-", "disk",
		"huge GET", len(hugeBody), hugeDur.Round(time.Millisecond), "truncated",
		"huge ?entry", len(entryBody), entryDur.Round(time.Millisecond), "full body",
	)

	if int64(len(raw)) >= jsonl.Size() {
		t.Fatalf("history GET %dB >= jsonl %dB", len(raw), jsonl.Size())
	}
	if len(raw) > 1_500_000 {
		t.Fatalf("history GET %dB too large", len(raw))
	}
	if len(runtimeBody) > 200_000 {
		t.Fatalf("runtime GET %dB too large", len(runtimeBody))
	}
	if first > 3*time.Second {
		t.Fatalf("history GET %s too slow", first)
	}
	if len(hugeBody) > 100_000 {
		t.Fatalf("truncated huge GET %dB", len(hugeBody))
	}
	if int64(len(entryBody)) < 300*1024 {
		t.Fatalf("full entry %dB", len(entryBody))
	}
	if int64(len(beforeBody)) >= jsonl.Size() {
		t.Fatalf("before GET %dB >= jsonl %dB", len(beforeBody), jsonl.Size())
	}
}

func countFlag(entries []any, key string) int {
	n := 0
	for _, item := range entries {
		m, _ := item.(map[string]any)
		if m[key] == true {
			n++
		}
	}
	return n
}
