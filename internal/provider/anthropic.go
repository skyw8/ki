package provider

import (
	"ki/internal/loop"
	"ki/internal/types"
	"ki/pkg/llmprotocol"
)

// AnthropicBody delegates wire encoding to the reusable protocol package.
func AnthropicBody(req loop.Request) map[string]any {
	return llmprotocol.AnthropicBody(toProtocolRequest(req))
}

// toAnthropicMessage is retained as a package-local inspection helper for Ki's
// provider tests; production encoding lives in llmprotocol.
func toAnthropicMessage(message types.Message) map[string]any {
	body := llmprotocol.AnthropicBody(toProtocolRequest(loop.Request{Messages: []types.Message{message}}))
	messages, _ := body["messages"].([]map[string]any)
	if len(messages) == 0 {
		return nil
	}
	return messages[0]
}

// anthropicToolResultBlock is retained as a package-local inspection helper
// for Ki's provider tests; production encoding lives in llmprotocol.
func anthropicToolResultBlock(message types.Message) map[string]any {
	body := llmprotocol.AnthropicBody(toProtocolRequest(loop.Request{Messages: []types.Message{message}}))
	messages, _ := body["messages"].([]map[string]any)
	if len(messages) == 0 {
		return nil
	}
	blocks, _ := messages[0]["content"].([]map[string]any)
	if len(blocks) == 0 {
		return nil
	}
	return blocks[0]
}
