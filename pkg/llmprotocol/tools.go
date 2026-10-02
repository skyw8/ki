package llmprotocol

import "fmt"

// validateToolSpecs rejects shapes an encoder would otherwise silently turn
// into a function with no schema. Model-specific custom support belongs to the
// caller; Responses as a protocol supports both function and custom tools.
func validateToolSpecs(tools []ToolSpec, api string) error {
	for _, tool := range tools {
		switch tool.Type {
		case "", "function":
		case "custom":
			if api != APIResponses {
				return fmt.Errorf("%s does not support custom tool declarations: %q", api, tool.Name)
			}
		default:
			return fmt.Errorf("%s unsupported tool type %q for %q", api, tool.Type, tool.Name)
		}
	}
	return nil
}

// customInputField finds the function envelope for a historical raw call.
// Inferring a single required string field avoids baking tool names (exec/code,
// apply_patch/input) or provider capabilities into the protocol layer.
func customInputField(tools []ToolSpec, name string) (field string, function bool, err error) {
	for _, tool := range tools {
		if tool.Name != name || tool.Type == "custom" {
			continue
		}
		properties, _ := tool.Parameters["properties"].(map[string]any)
		var required []string
		switch values := tool.Parameters["required"].(type) {
		case []string:
			required = values
		case []any:
			for _, value := range values {
				key, ok := value.(string)
				if !ok {
					return "", true, fmt.Errorf("cannot replay custom tool %q with invalid function schema", name)
				}
				required = append(required, key)
			}
		}
		if len(required) == 1 {
			field = required[0]
		} else if len(required) == 0 && len(properties) == 1 {
			for key := range properties {
				field = key
			}
		}
		property, _ := properties[field].(map[string]any)
		if field != "" && property["type"] == "string" {
			return field, true, nil
		}
		return "", true, fmt.Errorf("cannot replay custom tool %q: function schema needs a single string input field", name)
	}
	return "input", false, nil
}

func validateStructuredToolReplay(req Request) error {
	for _, message := range Replayable(req.Messages) {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls() {
			if call.ToolType == "custom" {
				if _, _, err := customInputField(req.Tools, call.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// adaptResponsesToolReplay changes historical custom calls only when the
// currently advertised tool is a function. Preserve native custom tools and
// opaque compaction items, and pair output types with their actual calls.
func adaptResponsesToolReplay(input []any, tools []ToolSpec) ([]any, error) {
	out := append([]any(nil), input...)
	callTypes := make(map[string]string)
	for i, raw := range out {
		item, err := responsesInputObject(raw)
		if err != nil {
			return nil, err
		}
		typ, _ := item["type"].(string)
		id, _ := item["call_id"].(string)
		switch typ {
		case "custom_tool_call":
			name, _ := item["name"].(string)
			field, function, err := customInputField(tools, name)
			if err != nil {
				return nil, err
			}
			if function {
				value, ok := item["input"].(string)
				if !ok {
					return nil, fmt.Errorf("custom tool %q input must be a string", name)
				}
				// Map-backed items and canonical raw items are caller-owned.
				item = cloneToolItem(item)
				item["type"] = "function_call"
				item["arguments"] = marshalArguments(map[string]any{field: value})
				delete(item, "input")
				out[i] = item
				typ = "function_call"
			}
			callTypes[id] = typ
		case "function_call":
			callTypes[id] = typ
		}
	}
	for i, raw := range out {
		item, _ := responsesInputObject(raw)
		typ, _ := item["type"].(string)
		if typ != "custom_tool_call_output" && typ != "function_call_output" {
			continue
		}
		id, _ := item["call_id"].(string)
		if callType := callTypes[id]; callType != "" && typ != callType+"_output" {
			item = cloneToolItem(item)
			item["type"] = callType + "_output"
			out[i] = item
		}
	}
	return out, nil
}

func cloneToolItem(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for key, value := range item {
		out[key] = value
	}
	return out
}
