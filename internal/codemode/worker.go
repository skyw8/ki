package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// ServeWorker serves one session over bounded, bidirectional NDJSON. Stdout is
// exclusively protocol traffic; the CLI entry must not print startup banners.
func ServeWorker(ctx context.Context, r io.Reader, w io.Writer, limits Limits) error {
	limits = limits.normalized()
	var mu sync.Mutex
	var session *Session
	var transport *peer
	handler := func(ctx context.Context, op string, data json.RawMessage) (any, error) {
		mu.Lock()
		s := session
		mu.Unlock()
		if op == "init" {
			mu.Lock()
			defer mu.Unlock()
			if session != nil {
				return nil, errors.New("code mode worker already initialized")
			}
			var requested Limits
			if err := json.Unmarshal(data, &requested); err != nil {
				return nil, err
			}
			requested = requested.normalized()
			if requested.MaxFrameBytes > limits.MaxFrameBytes {
				return nil, errors.New("requested worker frame budget exceeds bootstrap limit")
			}
			session = NewLocalSession(requested)
			return struct{}{}, nil
		}
		if s == nil {
			return nil, errors.New("code mode worker is not initialized")
		}
		switch op {
		case "execute":
			var req ExecuteRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			cb := Callbacks{
				Invoke: func(ctx context.Context, inv Invocation) (result ToolResult, err error) {
					err = transport.call(ctx, "invoke", inv, &result)
					return
				},
				Notify: func(ctx context.Context, n Notification) error { return transport.call(ctx, "notify", n, nil) },
			}
			// The request observer ends on yield, but the cell context must live
			// until terminal completion, cancellation, or worker disconnection.
			runCtx, cancel := context.WithCancel(transport.ctx)
			stop := context.AfterFunc(ctx, cancel)
			resp, err := s.Execute(runCtx, req, cb)
			stop()
			if err != nil {
				cancel()
			} else if !resp.Running() {
				cancel()
			}
			return resp, err
		case "wait":
			var req WaitRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			return s.Wait(ctx, req)
		case "terminateAll":
			return struct{}{}, s.TerminateAll(ctx)
		case "terminate":
			var req WaitRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			local := s.backend.(*localSession)
			local.mu.Lock()
			c := local.cells[req.CellID]
			local.mu.Unlock()
			if c == nil {
				return struct{}{}, nil
			}
			// Cancellation must not contend with an observer that is unwinding.
			c.stop()
			select {
			case <-c.done:
				return struct{}{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			return nil, errors.New("unknown code mode worker operation")
		}
	}
	// Publish the peer before starting its reader: callbacks may arrive as soon
	// as the parent sends init/execute on an already-buffered stream.
	ready := make(chan struct{})
	transport = newPeer(ctx, r, w, limits.MaxFrameBytes, func(ctx context.Context, op string, data json.RawMessage) (any, error) {
		<-ready
		return handler(ctx, op, data)
	})
	close(ready)
	<-transport.done
	mu.Lock()
	s := session
	mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), limits.DrainTimeout)
	defer cancel()
	_ = transport.drain(drainCtx)
	err := transport.err()
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
