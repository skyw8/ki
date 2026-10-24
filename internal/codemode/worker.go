package codemode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

func ServeWorker(ctx context.Context, r io.Reader, w io.Writer, limits Limits) error {
	limits = limits.normalized()
	var mu sync.Mutex
	var session *Session
	var transport *peer
	catalogs := make(map[string][]ToolDefinition)
	catalogBytes := 0
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
		case "catalog":
			var req catalogRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			raw, err := json.Marshal(req.Tools)
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256(raw)
			if req.ID != hex.EncodeToString(digest[:]) {
				return nil, errors.New("invalid catalog identity")
			}
			if err := validateRequest(ExecuteRequest{ParentCallID: "catalog", Source: "0", Tools: req.Tools}, s.backend.(*localSession).limits); err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			if _, exists := catalogs[req.ID]; !exists {
				if len(catalogs) >= 32 || catalogBytes+len(data) > limits.MaxFrameBytes*2 {
					return nil, errors.New("worker catalog registration budget exceeded")
				}
				catalogs[req.ID] = req.Tools
				catalogBytes += len(data)
			}
			return struct{}{}, nil
		case "execute":
			var req ExecuteRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			if req.CatalogID != "" {
				mu.Lock()
				definitions, exists := catalogs[req.CatalogID]
				req.Tools = definitions
				mu.Unlock()
				if !exists {
					return nil, errors.New("unknown registered tool catalog")
				}
			}
			cb := Callbacks{
				Invoke: func(ctx context.Context, inv Invocation) (result ToolResult, err error) {
					err = transport.call(ctx, "invoke", inv, &result)
					return
				},
				Notify: func(ctx context.Context, n Notification) error { return transport.call(ctx, "notify", n, nil) },
			}
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
			err := s.TerminateAll(ctx)
			mu.Lock()
			clear(catalogs)
			catalogBytes = 0
			mu.Unlock()
			return struct{}{}, err
		case "stopAll":
			s.backend.(*localSession).stopAll()
			return struct{}{}, nil
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
