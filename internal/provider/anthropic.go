package provider

import (
	"ki/internal/loop"
	"ki/internal/types"
	"ki/pkg/llmprotocol"
)

func AnthropicBody(req loop.Request) map[string]any {
	return llmprotocol.AnthropicBody(toProtocolRequest(req))
}

func toAnthropicMessage(message types.Message) map[string]any {
	body := llmprotocol.AnthropicBody(toProtocolRequest(loop.Request{Messages: []types.Message{message}}))
	messages, _ := body["messages"].([]map[string]any)
	if len(messages) == 0 {
		return nil
	}
	return messages[0]
}

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
