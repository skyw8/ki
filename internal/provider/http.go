package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ki/internal/loop"
	toolapi "ki/internal/tool"
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

func (l *Live) WithIdleTimeout(timeout time.Duration) *Live {
	l.client.IdleTimeout = timeout
	return l
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
	converted.API, converted.Provider, converted.Model = req.API, req.Provider, req.Model
	if err != nil {
		return converted, fmt.Errorf("protocol stream: %w", err)
	}
	if converted.Usage != nil && l.Model != nil {
		CalculateCost(*l.Model, converted.Usage)
	}
	return converted, nil
}

// Compact implements standalone OpenAI Responses compaction.
func (l *Live) Compact(ctx context.Context, req loop.Request) (CompactResult, error) {
	if l.client.API != llmprotocol.APIResponses {
		return CompactResult{}, fmt.Errorf("remote compaction requires Responses API")
	}
	protocolReq := toProtocolRequest(req)
	result, err := l.client.CompactResponses(ctx, llmprotocol.ResponsesCompactRequest{
		Model:        protocolReq.Model,
		Instructions: protocolReq.System,
		Window:       protocolReq.ResponsesWindow,
		Messages:     protocolReq.Messages,
	})
	if err != nil {
		return CompactResult{}, fmt.Errorf("protocol compact: %w", err)
	}
	out := CompactResult{Items: make([]json.RawMessage, len(result.Output))}
	for i, item := range result.Output {
		out.Items[i] = item.RawJSON()
	}
	if result.Usage != nil {
		out.Usage = fromProtocolUsage(result.Usage)
		if l.Model != nil {
			CalculateCost(*l.Model, out.Usage)
		}
	}
	return out, nil
}

func toProtocolRequest(req loop.Request) llmprotocol.Request {
	out := llmprotocol.Request{
		System:                    req.System,
		Messages:                  toProtocolMessages(req.Messages),
		Tools:                     toProtocolTools(req.Tools),
		Provider:                  req.Provider,
		Model:                     req.Model,
		MaxTokens:                 req.MaxTokens,
		ThinkingEffort:            req.ThinkingEffort,
		ThinkingFormat:            req.ThinkingFormat,
		MaxTokensField:            req.MaxTokensField,
		SupportsReasoningEffort:   req.SupportsReasoningEffort,
		ForceAdaptiveThinking:     req.ForceAdaptiveThinking,
		ThinkingLevelMap:          req.ThinkingLevelMap,
		ResponsesCompactThreshold: req.ResponsesCompactThreshold,
	}
	if len(req.ResponsesContext) > 0 {
		out.ResponsesWindow = make(llmprotocol.ResponsesWindow, len(req.ResponsesContext))
		for i, item := range req.ResponsesContext {
			out.ResponsesWindow[i] = append(llmprotocol.ResponsesItem(nil), item...)
		}
	}
	return out
}

func toProtocolTools(tools []toolapi.Spec) []llmprotocol.ToolSpec {
	if len(tools) == 0 {
		return nil
	}
	out := make([]llmprotocol.ToolSpec, len(tools))
	for i, tool := range tools {
		out[i] = llmprotocol.ToolSpec{
			Type: tool.Type, Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
		}
		if tool.Format != nil {
			out[i].Format = &llmprotocol.ToolFormat{Type: tool.Format.Type, Syntax: tool.Format.Syntax, Definition: tool.Format.Definition}
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
	if len(message.ResponsesItems) > 0 {
		out.ResponsesItems = make(llmprotocol.ResponsesWindow, len(message.ResponsesItems))
		for i, item := range message.ResponsesItems {
			out.ResponsesItems[i] = append(llmprotocol.ResponsesItem(nil), item...)
		}
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
	if len(message.ResponsesItems) > 0 {
		out.ResponsesItems = make([]json.RawMessage, len(message.ResponsesItems))
		for i, item := range message.ResponsesItems {
			out.ResponsesItems[i] = item.RawJSON()
		}
	}
	if message.Usage != nil {
		out.Usage = fromProtocolUsage(message.Usage)
	}
	if len(message.Content) > 0 {
		out.Content = make([]types.Content, len(message.Content))
		for i, content := range message.Content {
			out.Content[i] = fromProtocolContent(content)
		}
	}
	return out
}

func fromProtocolUsage(usage *llmprotocol.Usage) *types.Usage {
	if usage == nil {
		return nil
	}
	var cost *types.UsageCost
	if usage.Cost != nil {
		value := *usage.Cost
		cost = &types.UsageCost{Input: value.Input, Output: value.Output, CacheRead: value.CacheRead, CacheWrite: value.CacheWrite, Total: value.Total}
	}
	return &types.Usage{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite, TotalTokens: usage.TotalTokens, Cost: cost}
}

// ResponsesCheckpointItems returns the canonical suffix beginning with a
// server-emitted compaction item and including the assistant output that
// followed it in the same response.
func ResponsesCheckpointItems(message types.Message) ([]json.RawMessage, error) {
	if len(message.ResponsesItems) == 0 {
		return nil, nil
	}
	window, err := llmprotocol.ResponsesItemsForMessage(toProtocolMessage(message))
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(window))
	for i, item := range window {
		out[i] = item.RawJSON()
	}
	return out, nil
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
var _ Compactor = (*Live)(nil)
