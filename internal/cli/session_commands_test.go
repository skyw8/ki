package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"ki/internal/session"
	"ki/internal/types"
)

func TestSessionBrowseCommandsRegistered(t *testing.T) {
	cmd := newSessionCommand()
	for _, name := range []string{"list", "search", "show", "trace", "inspect", "compact", "fork"} {
		if found, _, err := cmd.Find([]string{name}); err != nil || found == cmd || found.Name() != name {
			t.Fatalf("command %q not registered: found=%v err=%v", name, found, err)
		}
	}
}

func TestWriteTraceShowsCacheMiss(t *testing.T) {
	var out bytes.Buffer
	rows := []session.TraceEntry{{
		Timestamp: "2026-09-29T03:11:16Z", Type: "message", Role: "assistant",
		Usage:     &types.Usage{Input: 20_000, CacheRead: 0},
		CacheMiss: &session.CacheMiss{MissedTokens: 20_000, Ratio: 1, Previous: 20_000, Prompt: 20_000},
		Preview:   "diagnostic",
	}}
	if err := writeTrace(&out, rows); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "cache-miss:100%") || !strings.Contains(got, "diagnostic") {
		t.Fatalf("trace output:\n%s", got)
	}
}

func TestSessionJSONLIsOneObjectPerLine(t *testing.T) {
	var out bytes.Buffer
	rows := []session.Info{{ID: "one"}, {ID: "two"}}
	if err := encodeJSONLines(&out, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	for _, line := range lines {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("invalid JSONL row %q: %v", line, err)
		}
	}
}

func TestOutputFlagsRejectConflict(t *testing.T) {
	if _, err := (outputFlags{format: "json", jsonl: true}).value(); err == nil {
		t.Fatal("expected conflicting format error")
	}
}
