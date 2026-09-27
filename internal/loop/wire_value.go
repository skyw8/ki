package loop

import (
	"encoding/json"

	"ki/internal/types"
)

// messageWireValue snapshots typed fields without copying growing strings.
// Mutable, provider-owned values still cross a JSON boundary: a reused argument
// map must not silently modify the previous patch base. Keep this projection in
// sync with Message/Content's JSON contract (the round-trip test covers fields).
func messageWireValue(m types.Message) (map[string]any, error) {
	v := map[string]any{"role": m.Role, "content": nil, "durationMs": m.DurationMs}
	stringsInto(v, map[string]string{
		"api": m.API, "provider": m.Provider, "model": m.Model, "responseId": m.ResponseID,
		"stopReason": m.StopReason, "errorMessage": m.ErrorMessage, "toolCallId": m.ToolCallID,
		"toolName": m.ToolName, "toolType": m.ToolType, "origin": m.Origin,
	})
	for k, n := range map[string]int64{"timestamp": m.Timestamp, "latencyMs": m.LatencyMs, "ttftMs": m.TTFTMs} {
		if n != 0 {
			v[k] = n
		}
	}
	if m.IsError {
		v["isError"] = true
	}
	if m.Content != nil {
		blocks := make([]any, len(m.Content))
		for i, c := range m.Content {
			b := map[string]any{"type": c.Type}
			stringsInto(b, map[string]string{
				"text": c.Text, "thinking": c.Thinking, "data": c.Data, "mimeType": c.MIMEType,
				"path": c.Path, "id": c.ID, "name": c.Name, "toolType": c.ToolType, "input": c.Input,
				"itemId": c.ItemID, "argumentsRaw": c.ArgumentsRaw, "thinkingSignature": c.ThinkingSignature,
				"thinkingData": c.ThinkingData, "textSignature": c.TextSignature,
			})
			if c.Size != 0 {
				b["size"] = c.Size
			}
			if len(c.Arguments) != 0 {
				args, err := snapshotJSON(c.Arguments)
				if err != nil {
					return nil, err
				}
				b["arguments"] = args
			}
			blocks[i] = b
		}
		v["content"] = blocks
	}
	if m.Usage != nil {
		u := m.Usage
		usage := map[string]any{"input": u.Input, "output": u.Output, "cacheRead": u.CacheRead, "cacheWrite": u.CacheWrite, "totalTokens": u.TotalTokens}
		if u.Cost != nil {
			c := u.Cost
			usage["cost"] = map[string]any{"input": c.Input, "output": c.Output, "cacheRead": c.CacheRead, "cacheWrite": c.CacheWrite, "total": c.Total}
		}
		v["usage"] = usage
	}
	if len(m.External) != 0 {
		ext := make(map[string]any, len(m.External))
		for k, s := range m.External {
			ext[k] = s
		}
		v["external"] = ext
	}
	if m.Details != nil {
		details, err := snapshotJSON(m.Details)
		if err != nil {
			return nil, err
		}
		v["details"] = details
	}
	return v, nil
}

func stringsInto(dst map[string]any, fields map[string]string) {
	for k, v := range fields {
		if v != "" {
			dst[k] = v
		}
	}
}

func snapshotJSON(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var snapshot any
	err = json.Unmarshal(raw, &snapshot)
	return snapshot, err
}

// Escaping can only make a string's JSON representation larger. This cheap
// estimate avoids serializing the snapshot merely to compare it with a delta.
func wireSizeLowerBound(v any) int {
	switch v := v.(type) {
	case string:
		return len(v) + 2
	case map[string]any:
		n := 2
		for k, value := range v {
			n += len(k) + 3 + wireSizeLowerBound(value)
		}
		return n
	case []any:
		n := 2
		for _, value := range v {
			n += wireSizeLowerBound(value)
		}
		return n
	default:
		return 1
	}
}
