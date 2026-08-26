package provider

import (
	"context"

	"ki/internal/loop"
	"ki/internal/types"
	"ki/pkg/llmprotocol"
)

// HTTPDoer is the transport used by live protocol clients.
type HTTPDoer = llmprotocol.HTTPDoer

// Live adapts the reusable protocol client to Ki's loop and message IR. Model
// metadata remains here because cost calculation is a Ki catalog concern.
type Live struct {
	client *llmprotocol.Client
	Model  *Model
}

// NewLive builds a live Ki provider adapter.
func NewLive(api, base, key string, doer HTTPDoer) *Live {
	return &Live{client: llmprotocol.NewClient(api, base, key, doer)}
}

// NewLiveModel creates a live adapter from a resolved Ki model.
func NewLiveModel(model Model, key string, doer HTTPDoer) *Live {
	return &Live{client: llmprotocol.NewClient(model.API, model.BaseURL, key, doer), Model: &model}
}

// Stream implements loop.Streamer by translating only at the package boundary.
func (l *Live) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	var protocolEmit func(llmprotocol.AssistantDelta) error
	if emit != nil {
		protocolEmit = func(delta llmprotocol.AssistantDelta) error {
			return emit(fromProtocolDelta(delta))
		}
	}
	message, err := l.client.Stream(ctx, toProtocolRequest(req), protocolEmit)
	converted := fromProtocolMessage(message)
	if err == nil && converted.Usage != nil && l.Model != nil {
		CalculateCost(*l.Model, converted.Usage)
	}
	return converted, err
}

func toProtocolRequest(req loop.Request) llmprotocol.Request {
	return llmprotocol.Request{
		System:                  req.System,
		Messages:                toProtocolMessages(req.Messages),
		Tools:                   toProtocolTools(req.Tools),
		Provider:                req.Provider,
		Model:                   req.Model,
		MaxTokens:               req.MaxTokens,
		ThinkingEffort:          req.ThinkingEffort,
		ThinkingFormat:          req.ThinkingFormat,
		MaxTokensField:          req.MaxTokensField,
		SupportsReasoningEffort: req.SupportsReasoningEffort,
		ForceAdaptiveThinking:   req.ForceAdaptiveThinking,
		ThinkingLevelMap:        req.ThinkingLevelMap,
	}
}

func toProtocolTools(tools []loop.ToolSpec) []llmprotocol.ToolSpec {
	if len(tools) == 0 {
		return nil
	}
	out := make([]llmprotocol.ToolSpec, len(tools))
	for i, tool := range tools {
		var format *llmprotocol.ToolFormat
		if tool.Format != nil {
			format = &llmprotocol.ToolFormat{
				Type: tool.Format.Type, Syntax: tool.Format.Syntax, Definition: tool.Format.Definition,
			}
		}
		out[i] = llmprotocol.ToolSpec{
			Type: tool.Type, Name: tool.Name, Description: tool.Description,
			Parameters: tool.Parameters, Format: format,
		}
	}
	return out
}

func toProtocolMessages(messages []types.Message) []llmprotocol.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]llmprotocol.Message, len(messages))
	for i, message := range messages {
		out[i] = toProtocolMessage(message)
	}
	return out
}

func toProtocolMessage(message types.Message) llmprotocol.Message {
	out := llmprotocol.Message{
		Role: message.Role, ResponseID: message.ResponseID, ToolCallID: message.ToolCallID,
		ToolName: message.ToolName, ToolType: message.ToolType, IsError: message.IsError,
		StopReason: message.StopReason, ErrorMessage: message.ErrorMessage,
	}
	if len(message.Content) > 0 {
		out.Content = make([]llmprotocol.Content, len(message.Content))
		for i, content := range message.Content {
			out.Content[i] = toProtocolContent(content)
		}
	}
	return out
}

func toProtocolContent(content types.Content) llmprotocol.Content {
	return llmprotocol.Content{
		Type: content.Type, Text: content.Text, Data: content.Data, MIMEType: content.MIMEType,
		Thinking: content.Thinking, ID: content.ID,
		Name: content.Name, ToolType: content.ToolType, Input: content.Input,
		Arguments: content.Arguments, ItemID: content.ItemID, ArgumentsRaw: content.ArgumentsRaw,
		ThinkingSignature: content.ThinkingSignature, ThinkingData: content.ThinkingData,
		TextSignature: content.TextSignature, StreamIndex: content.StreamIndex,
	}
}

func fromProtocolMessage(message llmprotocol.Message) types.Message {
	out := types.Message{
		Role: message.Role, ResponseID: message.ResponseID, StopReason: message.StopReason,
		ErrorMessage: message.ErrorMessage, ToolCallID: message.ToolCallID, ToolName: message.ToolName,
		ToolType: message.ToolType, IsError: message.IsError,
	}
	if message.Usage != nil {
		usage := *message.Usage
		var cost *types.UsageCost
		if message.Usage.Cost != nil {
			value := *message.Usage.Cost
			cost = &types.UsageCost{Input: value.Input, Output: value.Output, CacheRead: value.CacheRead, CacheWrite: value.CacheWrite, Total: value.Total}
		}
		out.Usage = &types.Usage{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite, TotalTokens: usage.TotalTokens, Cost: cost}
	}
	if len(message.Content) > 0 {
		out.Content = make([]types.Content, len(message.Content))
		for i, content := range message.Content {
			out.Content[i] = fromProtocolContent(content)
		}
	}
	return out
}

func fromProtocolContent(content llmprotocol.Content) types.Content {
	return types.Content{
		Type: content.Type, Text: content.Text, Data: content.Data, MIMEType: content.MIMEType,
		Thinking: content.Thinking, ID: content.ID,
		Name: content.Name, ToolType: content.ToolType, Input: content.Input,
		Arguments: content.Arguments, ItemID: content.ItemID, ArgumentsRaw: content.ArgumentsRaw,
		ThinkingSignature: content.ThinkingSignature, ThinkingData: content.ThinkingData,
		TextSignature: content.TextSignature, StreamIndex: content.StreamIndex,
	}
}

func fromProtocolDelta(delta llmprotocol.AssistantDelta) loop.AssistantDelta {
	return loop.AssistantDelta{
		Type: delta.Type, Delta: delta.Delta, ToolCallID: delta.ToolCallID,
		ToolName: delta.ToolName, Partial: fromProtocolMessage(delta.Partial),
	}
}

func replayable(messages []types.Message) []types.Message {
	protocolMessages := llmprotocol.Replayable(toProtocolMessages(messages))
	out := make([]types.Message, len(protocolMessages))
	for i, message := range protocolMessages {
		out[i] = fromProtocolMessage(message)
	}
	return out
}

var _ loop.Streamer = (*Live)(nil)
