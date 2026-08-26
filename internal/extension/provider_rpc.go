package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"ki/internal/loop"
	"ki/internal/types"
)

const providerStreamQueueSize = 128

type providerStreamPipe struct {
	events chan ProviderStreamEvent
	done   chan struct{}
}

func (c *rpcClient) streamProvider(ctx context.Context, req ProviderStreamRequest, emit func(loop.AssistantDelta) error) (types.Message, error) {
	requestID := "stream-" + strconv.FormatInt(c.idSeq.Add(1), 10)
	pipe := &providerStreamPipe{
		events: make(chan ProviderStreamEvent, providerStreamQueueSize),
		done:   make(chan struct{}),
	}
	c.providerStreamMu.Lock()
	if c.providerStreams == nil {
		c.providerStreams = map[string]*providerStreamPipe{}
	}
	c.providerStreams[requestID] = pipe
	c.providerStreamMu.Unlock()
	defer func() {
		close(pipe.done)
		c.providerStreamMu.Lock()
		delete(c.providerStreams, requestID)
		c.providerStreamMu.Unlock()
	}()

	startCtx, cancel := context.WithTimeout(ctx, timeoutProviderStart)
	defer cancel()
	var ack struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.call(startCtx, "provider.stream.start", map[string]any{
		"requestId": requestID,
		"request":   req,
	}, &ack); err != nil {
		return types.Message{}, err
	}
	if !ack.Accepted {
		return types.Message{}, fmt.Errorf("provider stream was not accepted")
	}

	acc := types.Message{
		Role:     "assistant",
		API:      req.Model.API,
		Provider: req.Model.Provider,
		Model:    req.Model.ID,
	}
	for {
		select {
		case <-ctx.Done():
			c.notify("provider.stream.cancel", map[string]any{"requestId": requestID})
			return acc, ctx.Err()
		case <-c.closed:
			return acc, fmt.Errorf("%w: sidecar closed", errRPC)
		case event := <-pipe.events:
			if event.RequestID != requestID {
				continue
			}
			switch event.Type {
			case "done":
				if event.Message != nil {
					mergeProviderMessage(&acc, *event.Message)
				}
				if acc.Role == "" {
					acc.Role = "assistant"
				}
				return acc, nil
			case "error":
				if event.Message != nil {
					mergeProviderMessage(&acc, *event.Message)
				}
				message := event.Error
				if message == "" {
					message = event.Reason
				}
				if message == "" {
					message = "provider stream failed"
				}
				return acc, errors.New(message)
			default:
				if event.Message != nil && event.Type == "start" {
					acc = *event.Message
				}
				if !applyProviderStreamEvent(&acc, event) {
					continue
				}
				if emit == nil {
					continue
				}
				delta := loop.AssistantDelta{
					Type:       event.Type,
					Delta:      event.Delta,
					ToolCallID: event.ToolCallID,
					ToolName:   event.ToolName,
					Partial:    acc,
				}
				if err := emit(delta); err != nil {
					c.notify("provider.stream.cancel", map[string]any{"requestId": requestID})
					return acc, err
				}
			}
		}
	}
}

func mergeProviderMessage(acc *types.Message, final types.Message) {
	previous := *acc
	if final.Role == "" {
		final.Role = previous.Role
	}
	if final.API == "" {
		final.API = previous.API
	}
	if final.Provider == "" {
		final.Provider = previous.Provider
	}
	if final.Model == "" {
		final.Model = previous.Model
	}
	if len(final.Content) == 0 {
		final.Content = previous.Content
	} else {
		for i := range final.Content {
			if i >= len(previous.Content) {
				continue
			}
			old, next := previous.Content[i], &final.Content[i]
			if next.Type == "" {
				next.Type = old.Type
			}
			if next.Text == "" {
				next.Text = old.Text
			}
			if next.Thinking == "" {
				next.Thinking = old.Thinking
			}
			if next.ID == "" {
				next.ID = old.ID
			}
			if next.Name == "" {
				next.Name = old.Name
			}
			if next.ToolType == "" {
				next.ToolType = old.ToolType
			}
			if next.Input == "" {
				next.Input = old.Input
			}
			if next.Arguments == nil {
				next.Arguments = old.Arguments
			}
			if next.ArgumentsRaw == "" {
				next.ArgumentsRaw = old.ArgumentsRaw
			}
			if next.ItemID == "" {
				next.ItemID = old.ItemID
			}
		}
	}
	*acc = final
}

func applyProviderStreamEvent(acc *types.Message, event ProviderStreamEvent) bool {
	if acc == nil {
		return false
	}
	switch event.Type {
	case "start":
		return event.Message != nil
	case "text_start", "text_delta", "text_end":
		block := ensureStreamContent(acc, event.ContentIndex, "text")
		if event.Type == "text_delta" {
			block.Text += event.Delta
		}
		return true
	case "thinking_start", "thinking_delta", "thinking_end":
		block := ensureStreamContent(acc, event.ContentIndex, "thinking")
		if event.Type == "thinking_delta" {
			block.Thinking += event.Delta
		}
		return true
	case "toolcall_start", "toolcall_delta", "toolcall_end", "custom_tool_call_input_delta":
		block := ensureStreamContent(acc, event.ContentIndex, "toolCall")
		if event.ToolCall != nil {
			incoming := *event.ToolCall
			if incoming.Type != "" {
				block.Type = incoming.Type
			}
			if incoming.Text != "" {
				block.Text = incoming.Text
			}
			if incoming.Thinking != "" {
				block.Thinking = incoming.Thinking
			}
			if incoming.ID != "" {
				block.ID = incoming.ID
			}
			if incoming.Name != "" {
				block.Name = incoming.Name
			}
			if incoming.ToolType != "" {
				block.ToolType = incoming.ToolType
			}
			if incoming.Input != "" {
				block.Input = incoming.Input
			}
			if incoming.Arguments != nil {
				block.Arguments = incoming.Arguments
			}
			if incoming.ArgumentsRaw != "" {
				block.ArgumentsRaw = incoming.ArgumentsRaw
			}
		}
		if block.Type == "" {
			block.Type = "toolCall"
		}
		if event.ToolCallID != "" {
			block.ID = event.ToolCallID
		}
		if event.ToolName != "" {
			block.Name = event.ToolName
		}
		if event.Type == "toolcall_delta" || event.Type == "custom_tool_call_input_delta" {
			if block.ToolType == "custom" || event.Type == "custom_tool_call_input_delta" {
				block.Input += event.Delta
			} else {
				block.ArgumentsRaw += event.Delta
				var args map[string]any
				if json.Unmarshal([]byte(block.ArgumentsRaw), &args) == nil {
					block.Arguments = args
				}
			}
		}
		return true
	default:
		return false
	}
}

func ensureStreamContent(acc *types.Message, index int, typ string) *types.Content {
	if index < 0 {
		index = 0
	}
	for len(acc.Content) <= index {
		acc.Content = append(acc.Content, types.Content{Type: typ})
	}
	block := &acc.Content[index]
	if block.Type == "" {
		block.Type = typ
	}
	return block
}
