package session

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// RuntimeTrace is a bounded last-known host snapshot, independent of the selected conversation branch.
type RuntimeTrace struct {
	Agent   any `json:"agent,omitempty"`
	Process any `json:"process,omitempty"`
}

func runtimeTrace(e Entry) *RuntimeTrace {
	if e.Type != "agent_updated" && e.Type != "process_updated" {
		return nil
	}
	raw, err := json.Marshal(e.Details)
	if err != nil {
		return nil
	}
	var value RuntimeTrace
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}
func runtimeKey(value *RuntimeTrace) string {
	if agent, ok := value.Agent.(map[string]any); ok {
		return "agent:" + fmt.Sprint(agent["agent_id"])
	}
	if process, ok := value.Process.(map[string]any); ok {
		return "process:" + fmt.Sprint(process["session_id"])
	}
	return ""
}

// Timing reports unions rather than adding parallel tool durations. Unknown
// retains model/input/unattributed intervals; it is not guessed resource time.
type Timing struct {
	ElapsedMs     int64 `json:"elapsedMs"`
	ToolMs        int64 `json:"toolMs"`
	MessageWaitMs int64 `json:"messageWaitMs"`
	UnknownMs     int64 `json:"unknownMs"`
}
type timedInterval struct{ start, end int64 }

func intervalUnion(values []timedInterval) int64 {
	if len(values) == 0 {
		return 0
	}
	slices.SortFunc(values, func(a, b timedInterval) int {
		if a.start < b.start {
			return -1
		}
		if a.start > b.start {
			return 1
		}
		return 0
	})
	first := values[0]
	total := int64(0)
	for _, next := range values[1:] {
		if next.start <= first.end {
			first.end = max(first.end, next.end)
		} else {
			total += first.end - first.start
			first = next
		}
	}
	return total + first.end - first.start
}
func analyzeTiming(entries []Entry) Timing {
	var tools, waits, all []timedInterval
	var start, end int64
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			continue
		}
		stamp := at.UnixMilli()
		if start == 0 {
			start = stamp
		}
		if e.Message.Role == "assistant" || e.Message.Role == "toolResult" {
			end = max(end, stamp)
		}
		m := e.Message
		if m.Role != "toolResult" || m.DurationMs <= 0 {
			continue
		}
		value := timedInterval{start: max(start, stamp-m.DurationMs), end: stamp}
		all = append(all, value)
		if m.ToolName == "wait_agent" || m.ToolName == "WaitAgent" {
			waits = append(waits, value)
		} else {
			tools = append(tools, value)
		}
	}
	elapsed := max(int64(0), end-start)
	return Timing{ElapsedMs: elapsed, ToolMs: intervalUnion(tools), MessageWaitMs: intervalUnion(waits), UnknownMs: max(int64(0), elapsed-intervalUnion(all))}
}
