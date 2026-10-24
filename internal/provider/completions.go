package provider

import (
	"ki/internal/loop"
	"ki/internal/types"
	"ki/pkg/llmprotocol"
)

func CompletionsBody(req loop.Request) map[string]any {
	return llmprotocol.CompletionsBody(toProtocolRequest(req))
}

func toOpenAIMessage(message types.Message) map[string]any {
	body := llmprotocol.CompletionsBody(toProtocolRequest(loop.Request{Messages: []types.Message{message}}))
	messages, _ := body["messages"].([]map[string]any)
	if len(messages) == 0 {
		return nil
	}
	return messages[0]
}
