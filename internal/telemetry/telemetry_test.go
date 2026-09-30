package telemetry

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ki/internal/types"
)

func TestRunWritesOTLPAndClassifiesCache(t *testing.T) {
	dir := t.TempDir()
	run := NewRun(dir, "session-1", "run-1")
	first := ModelRequest{
		Provider: "openai", Model: "gpt", API: "responses",
		StaticHash: "static", ShapeHash: "shape", HistoryChunks: []string{"a"},
		Usage: &types.Usage{Input: 5000, Output: 10, TotalTokens: 5010},
	}
	run.RecordModelRequest(first)
	run.RecordModelRequest(ModelRequest{
		Provider: "openai", Model: "gpt", API: "responses",
		StaticHash: "static", ShapeHash: "shape", HistoryChunks: []string{"a", "b"},
		Usage: &types.Usage{Input: 5100, Output: 10, TotalTokens: 5110},
	})
	run.RecordTool("Bash", "call-1", 12, true, ToolDiagnostic{
		Status: "completed", Kind: "command_nonzero", FaultDomain: "external_command",
	}, map[string]any{"process.exit.code": 1})
	run.Close()

	records := readRecords(t, filepath.Join(dir, FileName))
	if len(records) != 4 {
		t.Fatalf("records = %d, want 4", len(records))
	}
	firstAttrs := recordAttributes(t, records[0])
	if firstAttrs["ki.cache.classification"] != "first_request" {
		t.Fatalf("first classification = %#v", firstAttrs)
	}
	secondAttrs := recordAttributes(t, records[1])
	if secondAttrs["ki.cache.classification"] != "unexpected_full_miss" {
		t.Fatalf("second classification = %#v", secondAttrs)
	}
	toolAttrs := recordAttributes(t, records[2])
	if toolAttrs["ki.tool.kind"] != "command_nonzero" || toolAttrs["process.exit.code"] != "1" {
		t.Fatalf("tool attributes = %#v", toolAttrs)
	}
	summary := recordAttributes(t, records[3])
	if summary["ki.run.unexpected_cache_misses"] != "1" || summary["ki.run.command_nonzero"] != "1" {
		t.Fatalf("summary = %#v", summary)
	}
	logRecord := records[0]["resourceLogs"].([]any)[0].(map[string]any)["scopeLogs"].([]any)[0].(map[string]any)["logRecords"].([]any)[0].(map[string]any)
	trace, err := base64.StdEncoding.DecodeString(logRecord["traceId"].(string))
	if err != nil || len(trace) != 16 {
		t.Fatalf("trace id = %q: %v", logRecord["traceId"], err)
	}
}

func TestResetClassifiesExpectedBoundary(t *testing.T) {
	run := NewRun(t.TempDir(), "session-2", "run-2")
	run.RecordModelRequest(ModelRequest{
		StaticHash: "s", ShapeHash: "q", HistoryChunks: []string{"a"},
		Usage: &types.Usage{Input: 5000},
	})
	run.Reset("compaction")
	run.RecordModelRequest(ModelRequest{
		StaticHash: "s", ShapeHash: "q", HistoryChunks: []string{"z"},
		Usage: &types.Usage{Input: 100},
	})
	run.Close()
	records := readRecords(t, run.path)
	attrs := recordAttributes(t, records[1])
	if attrs["ki.cache.classification"] != "expected_reset.compaction" {
		t.Fatalf("classification = %#v", attrs)
	}
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var out []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		out = append(out, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func recordAttributes(t *testing.T, request map[string]any) map[string]string {
	t.Helper()
	resourceLogs := request["resourceLogs"].([]any)
	scopeLogs := resourceLogs[0].(map[string]any)["scopeLogs"].([]any)
	records := scopeLogs[0].(map[string]any)["logRecords"].([]any)
	raw := records[0].(map[string]any)["attributes"].([]any)
	out := map[string]string{}
	for _, item := range raw {
		kv := item.(map[string]any)
		value := kv["value"].(map[string]any)
		for _, field := range []string{"stringValue", "intValue", "doubleValue", "boolValue"} {
			if got, ok := value[field]; ok {
				out[kv["key"].(string)] = toString(got)
			}
		}
	}
	return out
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}
