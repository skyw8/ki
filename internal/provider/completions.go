package provider

import (
	"ki/internal/loop"
	"ki/internal/types"
	"ki/pkg/llmprotocol"
)

// CompletionsBody delegates wire encoding to the reusable protocol package.
func CompletionsBody(req loop.Request) map[string]any {
	return llmprotocol.CompletionsBody(toProtocolRequest(req))
}

// toOpenAIMessage is retained as a package-local inspection helper for Ki's
// provider tests; production encoding lives in llmprotocol.
func toOpenAIMessage(message types.Message) map[string]any {
	body := llmprotocol.CompletionsBody(toProtocolRequest(loop.Request{Messages: []types.Message{message}}))
	messages, _ := body["messages"].([]map[string]any)
	if len(messages) == 0 {
		return nil
	}
	return messages[0]
}
