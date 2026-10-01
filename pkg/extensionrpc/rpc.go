package extensionrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const MaxLineBytes = 65 * 1024 * 1024

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

type Handler func(context.Context, string, json.RawMessage) (any, error)

type requestIDKey struct{}

// RequestID returns the host ID for progress notifications from a handler.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type Peer struct {
	writer    *json.Encoder
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[string]chan Message
	requests  map[string]context.CancelFunc
	seq       atomic.Uint64
	done      chan struct{}
	closeOnce sync.Once
}

func New(out io.Writer) *Peer {
	return &Peer{writer: json.NewEncoder(out), pending: make(map[string]chan Message), requests: make(map[string]context.CancelFunc), done: make(chan struct{})}
}

func (p *Peer) send(msg Message) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.writer.Encode(msg)
}

func (p *Peer) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return p.send(Message{JSONRPC: "2.0", Method: method, Params: raw})
}

func (p *Peer) Call(ctx context.Context, method string, params, result any) error {
	// A canceled or closed call must not trigger an external host action.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-p.done:
		return io.EOF
	default:
	}
	// Host IDs are plain numbers/strings. A distinct namespace prevents an
	// inbound callback response from satisfying an unrelated host request.
	id, _ := json.Marshal(fmt.Sprintf("ext-%d", p.seq.Add(1)))
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan Message, 1)
	p.mu.Lock()
	p.pending[string(id)] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, string(id)); p.mu.Unlock() }()
	if err := p.send(Message{JSONRPC: "2.0", ID: id, Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return io.EOF
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error
		}
		if result == nil || len(msg.Result) == 0 {
			return nil
		}
		return json.Unmarshal(msg.Result, result)
	}
}

func (p *Peer) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, cancel := range p.requests {
			cancel()
		}
	})
}

func (p *Peer) Serve(ctx context.Context, in io.Reader, handler Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer p.Close()
	var requests sync.WaitGroup
	// Queue notifications without blocking the reader: a notification handler
	// may await a host response, which must still be read from the same pipe.
	var queueMu sync.Mutex
	var queue []Message
	wake := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			for {
				queueMu.Lock()
				if len(queue) == 0 {
					queueMu.Unlock()
					break
				}
				msg := queue[0]
				queue[0] = Message{}
				queue = queue[1:]
				queueMu.Unlock()
				_, _ = handler(ctx, msg.Method, msg.Params)
			}
		}
	}()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), MaxLineBytes)
	for sc.Scan() {
		var msg Message
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.JSONRPC != "2.0" {
			continue
		}
		if msg.Method == "" {
			p.mu.Lock()
			ch := p.pending[string(msg.ID)]
			delete(p.pending, string(msg.ID))
			p.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.Method == "cancel" {
			var params struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			p.mu.Lock()
			stop := p.requests[string(params.ID)]
			p.mu.Unlock()
			if stop != nil {
				stop()
			}
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			queueMu.Lock()
			queue = append(queue, msg)
			queueMu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
			continue
		}
		requestCtx, stop := context.WithCancel(ctx)
		var requestID string
		if json.Unmarshal(msg.ID, &requestID) != nil {
			requestID = string(msg.ID)
		}
		requestCtx = context.WithValue(requestCtx, requestIDKey{}, requestID)
		p.mu.Lock()
		p.requests[string(msg.ID)] = stop
		p.mu.Unlock()
		requests.Add(1)
		go func(msg Message) {
			defer requests.Done()
			defer stop()
			defer func() { p.mu.Lock(); delete(p.requests, string(msg.ID)); p.mu.Unlock() }()
			value, err := handler(requestCtx, msg.Method, msg.Params)
			response := Message{JSONRPC: "2.0", ID: msg.ID}
			if err != nil {
				response.Error = &Error{Code: -32000, Message: err.Error()}
				var rpcErr *Error
				if errors.As(err, &rpcErr) {
					response.Error = rpcErr
				}
			} else {
				if value == nil {
					value = map[string]any{}
				}
				response.Result, err = json.Marshal(value)
				if err != nil {
					response.Error = &Error{Code: -32000, Message: err.Error()}
				}
			}
			_ = p.send(response)
		}(msg)
	}
	// A piped request closes stdin before its goroutine can write the reply.
	// Cancel work immediately, then let quick handlers flush without allowing
	// an uncooperative handler or blocked stdout to prevent process shutdown.
	cancel()
	p.Close()
	drained := make(chan struct{})
	go func() { requests.Wait(); close(drained) }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-drained:
	case <-timer.C:
	}
	return sc.Err()
}
