package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"ki/internal/loop"
)

const taskOutputPrompt = `Retrieves output and status for a background task started by Bash or Monitor.

Use block=false for an immediate snapshot. Use block=true to wait for completion up to timeout milliseconds. For a running task, output contains the captured output so far and output_file can be read with Read.`

type taskOutputTool struct{ jobs *JobStore }

func (taskOutputTool) Name() string        { return "TaskOutput" }
func (taskOutputTool) Description() string { return "Retrieve output from a background task." }
func (taskOutputTool) Snippet() string     { return "Wait for or inspect a background task" }
func (taskOutputTool) Prompt() string      { return taskOutputPrompt }
func (taskOutputTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"task_id"},
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": "The task ID returned by Bash or Monitor."},
			"block":   map[string]any{"type": "boolean", "description": "Whether to wait for the task to finish. Defaults to true."},
			"timeout": map[string]any{"type": "number", "minimum": 0, "maximum": 600000, "description": "Maximum wait time in milliseconds. Defaults to 30000."},
		},
	}
}
func (t taskOutputTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

type taskOutputResponse struct {
	RetrievalStatus string        `json:"retrieval_status"`
	Task            *TaskSnapshot `json:"task"`
}

func (t taskOutputTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	if t.jobs == nil {
		return errRes("task store is unavailable")
	}
	id := stringArg(args, "task_id", "")
	if id == "" {
		return errRes("task_id is required")
	}
	block := true
	if v, ok := args["block"].(bool); ok {
		block = v
	}
	snapshot, ok := t.jobs.Get(id)
	if !ok {
		return errRes(fmt.Sprintf("task %s not found", id))
	}
	status := "success"
	if !block && !isTerminal(snapshot.Status) {
		status = "not_ready"
	}
	if block && !isTerminal(snapshot.Status) {
		timeout := 30000
		if v, present := args["timeout"]; present {
			parsed, valid := asInt(v)
			if !valid || parsed < 0 {
				return errRes("timeout must be a non-negative number")
			}
			timeout = min(parsed, 600000)
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		waited, err := t.jobs.Wait(waitCtx, id)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			if errors.Is(err, context.Canceled) {
				return errRes("TaskOutput aborted")
			}
			return errRes(err.Error())
		}
		snapshot = waited
		if errors.Is(err, context.DeadlineExceeded) && !isTerminal(snapshot.Status) {
			status = "timeout"
		}
	}
	if output, ok := t.jobs.Output(id); ok {
		snapshot.Output = output
	}
	return taskResult(taskOutputResponse{RetrievalStatus: status, Task: &snapshot})
}

type taskStopTool struct{ jobs *JobStore }

func (taskStopTool) Name() string        { return "TaskStop" }
func (taskStopTool) Description() string { return "Stop a running background task." }
func (taskStopTool) Snippet() string     { return "Terminate a background task" }
func (taskStopTool) Prompt() string {
	return "Stops a running background task by ID. Use this for long-running commands that should no longer continue."
}
func (taskStopTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"task_id":  map[string]any{"type": "string", "description": "The task ID to stop."},
			"shell_id": map[string]any{"type": "string", "description": "Deprecated alias for task_id."},
		},
	}
}
func (t taskStopTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t taskStopTool) Execute(_ context.Context, args map[string]any) loop.ToolResult {
	if t.jobs == nil {
		return errRes("task store is unavailable")
	}
	id := stringArg(args, "task_id", "")
	if id == "" {
		id = stringArg(args, "shell_id", "")
	}
	if id == "" {
		return errRes("task_id is required")
	}
	snapshot, err := t.jobs.Stop(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errRes(fmt.Sprintf("task %s not found", id))
		}
		return errRes(err.Error())
	}
	return taskResult(map[string]any{
		"message":   fmt.Sprintf("Task %s stopped", id),
		"task_id":   snapshot.TaskID,
		"task_type": snapshot.TaskType,
		"status":    snapshot.Status,
		"command":   snapshot.Command,
	})
}

type monitorTool struct {
	jobs *JobStore
	cwd  string
}

func (monitorTool) Name() string        { return "Monitor" }
func (monitorTool) Description() string { return "Run a command and stream each stdout/stderr update." }
func (monitorTool) Snippet() string     { return "Stream output from a long-running command" }
func (monitorTool) Prompt() string {
	return "Runs a monitoring command as a background task and streams output updates. Use Bash with run_in_background for a one-shot command that only needs a completion result."
}
func (monitorTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"command"},
		"properties": map[string]any{
			"command":     map[string]any{"type": "string", "description": "Command or script whose output should be streamed."},
			"description": map[string]any{"type": "string", "description": "Short description of the monitored stream."},
		},
	}
}
func (t monitorTool) Validate(args map[string]any) error {
	return validateArgs(t.Parameters(), t.Name(), args)
}

func (t monitorTool) Execute(ctx context.Context, args map[string]any) loop.ToolResult {
	return t.ExecuteWithProgress(ctx, args, nil)
}

func (t monitorTool) ExecuteWithProgress(ctx context.Context, args map[string]any, emit func(any)) loop.ToolResult {
	if t.jobs == nil {
		return errRes("task store is unavailable")
	}
	command := stringArg(args, "command", "")
	if command == "" {
		return errRes("command is required")
	}
	description := stringArg(args, "description", command)
	id, _, err := t.jobs.Start(ctx, t.cwd, command, description, "local_bash")
	if err != nil {
		return errRes(err.Error())
	}
	updates, stop, err := t.jobs.Subscribe(id)
	if err != nil {
		return errRes(err.Error())
	}
	done, ok := t.jobs.Done(id)
	if !ok {
		stop()
		return errRes(fmt.Sprintf("task %s not found", id))
	}
	defer stop()
	for {
		select {
		case update := <-updates:
			if update.Delta != "" && emit != nil {
				emit(map[string]any{
					"type": "monitor_progress", "task_id": id,
					"output": update.Delta, "task": update.Snapshot,
				})
			}
		case <-done:
			for {
				select {
				case update := <-updates:
					if update.Delta != "" && emit != nil {
						emit(map[string]any{"type": "monitor_progress", "task_id": id, "output": update.Delta, "task": update.Snapshot})
					}
				default:
					snapshot, _ := t.jobs.Get(id)
					if output, ok := t.jobs.Output(id); ok {
						snapshot.Output = output
					}
					return taskResult(snapshot)
				}
			}
		case <-ctx.Done():
			_, _ = t.jobs.Stop(id)
			return errRes("Monitor aborted")
		}
	}
}

func taskResult(value any) loop.ToolResult {
	b, err := json.Marshal(value)
	if err != nil {
		return errRes(err.Error())
	}
	return okRes(string(b))
}
