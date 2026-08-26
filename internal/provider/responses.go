package provider

import (
	"ki/internal/loop"
	"ki/internal/types"
	"ki/pkg/llmprotocol"
)

// ResponsesBody delegates wire encoding to the reusable protocol package.
func ResponsesBody(req loop.Request) map[string]any {
	return llmprotocol.ResponsesBody(toProtocolRequest(req))
}

// toResponsesItems is retained as a package-local inspection helper for Ki's
// provider tests; production encoding lives in llmprotocol.
func toResponsesItems(message types.Message) []any {
	body := llmprotocol.ResponsesBody(toProtocolRequest(loop.Request{Messages: []types.Message{message}}))
	items, _ := body["input"].([]any)
	return items
}
