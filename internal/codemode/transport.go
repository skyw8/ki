package codemode

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

type wireFrame struct {
	Kind  string          `json:"kind"`
	ID    uint64          `json:"id"`
	Op    string          `json:"op,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type wireResponse struct {
	data json.RawMessage
	err  error
}
type inboundRequest struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// A peer always reads responses independently from callback work. Otherwise a
// tool callback waiting for a response can deadlock the bidirectional pipe.
type peer struct {
	ctx      context.Context
	cancel   context.CancelFunc
	reader   io.Reader
	writer   io.Writer
	maxFrame int
	handler  func(context.Context, string, json.RawMessage) (any, error)
	out      chan []byte
	done     chan struct{}
	next     atomic.Uint64
	mu       sync.Mutex
	pending  map[uint64]chan wireResponse
	inbound  map[uint64]*inboundRequest
	failure  error
}

func newPeer(ctx context.Context, r io.Reader, w io.Writer, maxFrame int, handler func(context.Context, string, json.RawMessage) (any, error)) *peer {
	ctx, cancel := context.WithCancel(ctx)
	p := &peer{ctx: ctx, cancel: cancel, reader: r, writer: w, maxFrame: maxFrame, handler: handler, out: make(chan []byte, 4), done: make(chan struct{}), pending: make(map[uint64]chan wireResponse), inbound: make(map[uint64]*inboundRequest)}
	go p.read()
	go p.write()
	go func() { <-ctx.Done(); p.fail(ctx.Err()) }()
	return p
}

func (p *peer) fail(err error) {
	p.mu.Lock()
	if p.failure != nil {
		p.mu.Unlock()
		return
	}
	if err == nil {
		err = io.EOF
	}
	p.failure = err
	close(p.done)
	for id, ch := range p.pending {
		ch <- wireResponse{err: err}
		delete(p.pending, id)
	}
	for _, req := range p.inbound {
		req.cancel()
	}
	p.mu.Unlock()
	p.cancel()
	if c, ok := p.reader.(io.Closer); ok {
		_ = c.Close()
	}
	if c, ok := p.writer.(io.Closer); ok {
		_ = c.Close()
	}
}

func (p *peer) send(ctx context.Context, frame wireFrame) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(b) > p.maxFrame {
		return errors.New("code mode IPC frame exceeds limit")
	}
	b = append(b, '\n')
	select {
	case <-p.done:
		return p.err()
	default:
	}
	select {
	case p.out <- b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return p.err()
	}
}

func (p *peer) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	return errors.New("code mode transport is closed")
}

func (p *peer) call(ctx context.Context, op string, data any, out any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	id := p.next.Add(1)
	ch := make(chan wireResponse, 1)
	p.mu.Lock()
	if p.failure != nil {
		err = p.failure
		p.mu.Unlock()
		return err
	}
	if len(p.pending) >= 128 {
		p.mu.Unlock()
		return errors.New("code mode IPC request limit reached")
	}
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	if err = p.send(ctx, wireFrame{Kind: "request", ID: id, Op: op, Data: raw}); err != nil {
		return err
	}
	select {
	case response := <-ch:
		if response.err != nil {
			return response.err
		}
		if out != nil && len(response.data) > 0 {
			return json.Unmarshal(response.data, out)
		}
		return nil
	case <-ctx.Done():
		// Best-effort cancellation is followed by caller-side lifecycle cleanup;
		// abandoning the observer never grants the worker a new capability.
		cancelCtx, cancel := context.WithTimeout(p.ctx, 50*time.Millisecond)
		_ = p.send(cancelCtx, wireFrame{Kind: "cancel", ID: id})
		cancel()
		return ctx.Err()
	case <-p.done:
		return p.err()
	}
}

func (p *peer) read() {
	scanner := bufio.NewScanner(p.reader)
	scanner.Buffer(make([]byte, 4096), p.maxFrame+1)
	for scanner.Scan() {
		if len(scanner.Bytes()) > p.maxFrame {
			p.fail(errors.New("code mode IPC frame exceeds limit"))
			return
		}
		var frame wireFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			p.fail(fmt.Errorf("invalid code mode IPC: %w", err))
			return
		}
		if frame.ID == 0 {
			p.fail(errors.New("invalid code mode IPC request ID"))
			return
		}
		switch frame.Kind {
		case "response":
			p.mu.Lock()
			ch := p.pending[frame.ID]
			delete(p.pending, frame.ID)
			p.mu.Unlock()
			if ch != nil {
				var err error
				if frame.Error != "" {
					err = errors.New(frame.Error)
				}
				ch <- wireResponse{data: frame.Data, err: err}
			}
		case "cancel":
			p.mu.Lock()
			req := p.inbound[frame.ID]
			p.mu.Unlock()
			if req != nil {
				req.cancel()
			}
		case "request":
			p.accept(frame)
		default:
			p.fail(errors.New("invalid code mode IPC frame kind"))
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	p.fail(err)
}

func (p *peer) accept(frame wireFrame) {
	p.mu.Lock()
	if p.failure != nil {
		p.mu.Unlock()
		return
	}
	if _, exists := p.inbound[frame.ID]; exists {
		p.mu.Unlock()
		p.fail(errors.New("duplicate code mode IPC request ID"))
		return
	}
	if len(p.inbound) >= 128 {
		p.mu.Unlock()
		p.fail(errors.New("code mode IPC inbound request limit reached"))
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	req := &inboundRequest{cancel: cancel, done: make(chan struct{})}
	p.inbound[frame.ID] = req
	p.mu.Unlock()
	go func() {
		defer func() { cancel(); p.mu.Lock(); delete(p.inbound, frame.ID); close(req.done); p.mu.Unlock() }()
		result, err := p.invokeHandler(ctx, frame.Op, frame.Data)
		response := wireFrame{Kind: "response", ID: frame.ID}
		if err == nil {
			response.Data, err = json.Marshal(result)
		}
		if err != nil {
			response.Data = nil
			response.Error = boundedError(err, 8192)
		}
		if err := p.send(p.ctx, response); err != nil {
			p.fail(err)
		}
	}()
}

func (p *peer) invokeHandler(ctx context.Context, op string, data json.RawMessage) (result any, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("code mode IPC handler panic: %v", v)
		}
	}()
	if p.handler == nil {
		return nil, errors.New("code mode IPC operation unavailable")
	}
	return p.handler(ctx, op, data)
}

func (p *peer) write() {
	for {
		select {
		case <-p.done:
			return
		case data := <-p.out:
			for len(data) > 0 {
				n, err := p.writer.Write(data)
				if err != nil {
					p.fail(err)
					return
				}
				if n == 0 {
					p.fail(io.ErrShortWrite)
					return
				}
				data = data[n:]
			}
		}
	}
}

func (p *peer) drain(ctx context.Context) error {
	p.mu.Lock()
	requests := make([]*inboundRequest, 0, len(p.inbound))
	for _, req := range p.inbound {
		requests = append(requests, req)
	}
	p.mu.Unlock()
	for _, req := range requests {
		select {
		case <-req.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (p *peer) cancelInbound() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, req := range p.inbound {
		req.cancel()
	}
}
