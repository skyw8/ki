package provider

import (
	"ki/internal/loop"
	"ki/internal/types"
	"ki/pkg/llmprotocol"
)

func ResponsesBody(req loop.Request) map[string]any {
	return llmprotocol.ResponsesBody(toProtocolRequest(req))
}

func toResponsesItems(message types.Message) []any {
	body := llmprotocol.ResponsesBody(toProtocolRequest(loop.Request{Messages: []types.Message{message}}))
	items, _ := body["input"].([]any)
	return items
}
