package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedRPCBuffer struct {
	mu sync.Mutex
	bytes.Buffer
	wrote chan struct{}
}

func (b *lockedRPCBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.Buffer.Write(p)
	select {
	case b.wrote <- struct{}{}:
	default:
	}
	return n, err
}

func (b *lockedRPCBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestRPCPipedRequestFlushesReplyAtEOF(t *testing.T) {
	var output lockedRPCBuffer
	rpc := newStdioRPC(nil, &output)
	rpc.onRequest(func(method string, params json.RawMessage) (any, error) {
		return map[string]any{"accepted": method == "initialize"}, nil
	})
	input := strings.NewReader(`{"jsonrpc":"2.0","id":"init","method":"initialize"}` + "\n")
	if err := rpc.serve(t.Context(), input); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var reply rpcMessage
	if err := json.Unmarshal([]byte(output.snapshot()), &reply); err != nil || reply.ID != "init" || string(reply.Result) != `{"accepted":true}` {
		t.Fatalf("piped reply=%s error=%v", output.snapshot(), err)
	}
}

func TestRPCEOFUnblocksReverseCall(t *testing.T) {
	output := lockedRPCBuffer{wrote: make(chan struct{}, 1)}
	rpc := newStdioRPC(nil, &output)
	returned := make(chan error, 1)
	rpc.onRequest(func(method string, params json.RawMessage) (any, error) {
		err := rpc.call(context.Background(), "session.get", map[string]any{}, nil)
		returned <- err
		return nil, err
	})
	input, writeInput := io.Pipe()
	defer input.Close()
	defer writeInput.Close()
	done := make(chan error, 1)
	go func() { done <- rpc.serve(t.Context(), input) }()
	if _, err := io.WriteString(writeInput, `{"jsonrpc":"2.0","id":"callback","method":"test"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-output.wrote:
	case <-time.After(3 * time.Second):
		t.Fatal("host callback was not sent")
	}
	if err := writeInput.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	select {
	case err := <-returned:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	default:
		t.Fatal("request awaiting a host callback was not drained")
	}
	before := output.snapshot()
	if err := rpc.call(t.Context(), "session.enqueue", nil, nil); !errors.Is(err, io.EOF) || output.snapshot() != before {
		t.Fatalf("closed call wrote to host: %v", err)
	}
}

func TestRPCEOFDrainIsBounded(t *testing.T) {
	var output lockedRPCBuffer
	rpc := newStdioRPC(nil, &output)
	release := make(chan struct{})
	defer close(release)
	rpc.onRequest(func(method string, params json.RawMessage) (any, error) {
		<-release
		return map[string]any{}, nil
	})
	done := make(chan error, 1)
	go func() {
		input := strings.NewReader(`{"jsonrpc":"2.0","id":"stalled","method":"test"}` + "\n")
		done <- rpc.serve(t.Context(), input)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("EOF drain was held by a stalled request")
	}
}
