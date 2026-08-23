package tools

import "strings"

type taskTruncationDetails struct {
	Truncated   bool  `json:"truncated"`
	TotalBytes  int64 `json:"total_bytes"`
	TotalLines  int64 `json:"total_lines"`
	OutputBytes int   `json:"output_bytes"`
	OutputLines int   `json:"output_lines"`
}

type taskDetails struct {
	TaskID          string                 `json:"task_id,omitempty"`
	Status          TaskStatus             `json:"status,omitempty"`
	RetrievalStatus string                 `json:"retrieval_status,omitempty"`
	TimedOut        bool                   `json:"timed_out,omitempty"`
	Cancelled       bool                   `json:"cancelled,omitempty"`
	ExitCode        *int                   `json:"exit_code,omitempty"`
	OutputFile      string                 `json:"output_file,omitempty"`
	Truncation      *taskTruncationDetails `json:"truncation,omitempty"`
}

func boundedTaskSnapshot(snapshot TaskSnapshot) (TaskSnapshot, *taskTruncationDetails) {
	output, note := truncateTaskTail(snapshot.Output, snapshot.Bytes, snapshot.Lines, snapshot.OutputFile)
	visible := output
	snapshot.Output = output + note
	totalLines := snapshot.Lines
	if snapshot.Bytes > 0 && !strings.HasSuffix(snapshot.Output, "\n") {
		totalLines++
	}
	details := &taskTruncationDetails{
		Truncated: note != "", TotalBytes: snapshot.Bytes, TotalLines: totalLines,
		OutputBytes: len(visible), OutputLines: len(splitOutputLines(visible)),
	}
	return snapshot, details
}

func detailsForTask(snapshot TaskSnapshot, retrieval string) taskDetails {
	bounded, truncation := boundedTaskSnapshot(snapshot)
	return taskDetails{TaskID: bounded.TaskID, Status: bounded.Status, RetrievalStatus: retrieval, ExitCode: bounded.ExitCode, OutputFile: bounded.OutputFile, Truncation: truncation}
}
