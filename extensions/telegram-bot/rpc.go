package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// stdioRPC is the small JSON-RPC transport shared by the Telegram sidecar.
// Host requests and sidecar-initiated session calls may be concurrent.
type stdioRPC struct {
	encMu   sync.Mutex
	enc     *json.Encoder
	pending sync.Map
	seq     atomic.Uint64
	handler func(string, json.RawMessage) (any, error)
}

func newStdioRPC(in io.Reader, out io.Writer) *stdioRPC {
	return &stdioRPC{enc: json.NewEncoder(out)}
}

func (r *stdioRPC) onRequest(handler func(string, json.RawMessage) (any, error)) {
	r.handler = handler
}

func (r *stdioRPC) serve(ctx context.Context, in io.Reader) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var msg rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil || msg.JSONRPC == "" {
			continue
		}
		if msg.Method != "" {
			if msg.ID == nil {
				if msg.Method == "lifecycle.event" {
					// The host writes lifecycle notifications in loop order. Preserve
					// that order while the handler queues the actual Telegram work.
					r.dispatchNotification(msg)
					continue
				}
				go r.dispatchNotification(msg)
				continue
			}
			go r.dispatchRequest(msg)
			continue
		}
		if msg.ID == nil {
			continue
		}
		if pending, ok := r.pending.LoadAndDelete(rpcID(msg.ID)); ok {
			pending.(chan rpcMessage) <- msg
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (r *stdioRPC) dispatchNotification(msg rpcMessage) {
	if r.handler == nil {
		return
	}
	_, _ = r.handler(msg.Method, msg.Params)
}

func (r *stdioRPC) dispatchRequest(msg rpcMessage) {
	var result any
	var err error
	if r.handler == nil {
		err = fmt.Errorf("rpc handler unavailable")
	} else {
		result, err = r.handler(msg.Method, msg.Params)
	}
	if err != nil {
		r.replyError(msg.ID, err.Error())
		return
	}
	r.reply(msg.ID, result)
}

func (r *stdioRPC) call(ctx context.Context, method string, params any, result any) error {
	id := fmt.Sprintf("tg-%d", r.seq.Add(1))
	ch := make(chan rpcMessage, 1)
	r.pending.Store(id, ch)
	if err := r.send(rpcMessage{JSONRPC: "2.0", ID: id, Method: method, Params: mustJSON(params)}); err != nil {
		r.pending.Delete(id)
		return err
	}
	defer r.pending.Delete(id)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		if result == nil || len(msg.Result) == 0 || string(msg.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(msg.Result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
		return nil
	}
}

func (r *stdioRPC) reply(id any, result any) {
	_ = r.send(rpcMessage{JSONRPC: "2.0", ID: id, Result: mustJSON(result)})
}

func (r *stdioRPC) replyError(id any, message string) {
	_ = r.send(rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32000, Message: message}})
}

func (r *stdioRPC) send(msg rpcMessage) error {
	r.encMu.Lock()
	defer r.encMu.Unlock()
	return r.enc.Encode(msg)
}

func mustJSON(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return b
}

func rpcID(id any) string {
	switch value := id.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return fmt.Sprintf("%.0f", value)
	default:
		return fmt.Sprint(value)
	}
}

func reportError(message string) {
	_, _ = fmt.Fprintln(os.Stderr, "telegram-bot:", message)
}
