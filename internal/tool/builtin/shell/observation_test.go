package shelltools

import (
	"os"
	"strings"
	"testing"

	"ki/internal/tool/output"
)

func TestTerminalQuotaReportsPrefixAndPreservesIncrementalSummary(t *testing.T) {
	store, err := output.NewWithConfig(output.Config{Root: t.TempDir(), MaxProcessFileBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tool := testExec(t, store)
	result := tool.Execute(t.Context(), map[string]any{"cmd": "printf HEADabcdefghTAIL", "max_output_tokens": 4, "include_summary": true})
	s := execSnapshot(t, result)
	if !result.IsError || s.ExitCode == nil || *s.ExitCode != 0 || !s.OutputFileIncomplete || s.OutputFileBytes != 8 || !strings.Contains(s.Error, "quota") {
		t.Fatalf("quota: %+v", s)
	}
	raw, err := os.ReadFile(s.OutputFile)
	if err != nil || string(raw) != "HEADabcd" {
		t.Fatalf("raw prefix: %q %v", raw, err)
	}
	if s.Output != "HEADabcd" || !strings.Contains(s.OutputSummary, "H") || !strings.Contains(s.OutputSummary, "L") {
		t.Fatalf("summary: %+v", s)
	}
	next := execSnapshot(t, (writeStdinTool{processes: tool.processes}).Execute(t.Context(), map[string]any{"session_id": s.SessionID, "max_output_tokens": 4, "include_summary": true}))
	if next.OutputOffset != 8 || next.Output != "efghTAIL" || next.EffectiveOutputTokens != 4 || next.RequestedOutputTokens != 4 {
		t.Fatalf("cursor: %+v", next)
	}
}
