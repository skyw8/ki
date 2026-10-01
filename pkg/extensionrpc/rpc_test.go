package extensionrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type testConnection struct {
	peer    *Peer
	input   *io.PipeWriter
	output  *io.PipeReader
	encoder *json.Encoder
	decoder *json.Decoder
	done    chan error
}

func connect(t *testing.T, handler Handler) *testConnection {
	t.Helper()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	c := &testConnection{peer: New(outputWriter), input: inputWriter, output: outputReader, encoder: json.NewEncoder(inputWriter), decoder: json.NewDecoder(outputReader), done: make(chan error, 1)}
	go func() { c.done <- c.peer.Serve(t.Context(), inputReader, handler); outputWriter.Close() }()
	t.Cleanup(func() {
		c.peer.Close()
		inputWriter.Close()
		inputReader.Close()
		outputReader.Close()
		outputWriter.Close()
	})
	return c
}
func (c *testConnection) write(t *testing.T, msg Message) {
	t.Helper()
	if err := c.encoder.Encode(msg); err != nil {
		t.Fatal(err)
	}
}
func (c *testConnection) read(t *testing.T) Message { return c.readWithin(t, 5*time.Second) }
func (c *testConnection) readWithin(t *testing.T, timeout time.Duration) Message {
	t.Helper()
	result := make(chan struct {
		message Message
		err     error
	}, 1)
	go func() {
		var m Message
		err := c.decoder.Decode(&m)
		result <- struct {
			message Message
			err     error
		}{m, err}
	}()
	select {
	case r := <-result:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.message
	case <-time.After(timeout):
		t.Fatal("RPC read stalled")
		return Message{}
	}
}
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func TestConcurrentCallsCorrelateOutOfOrderReplies(t *testing.T) {
	c := connect(t, func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
	const total = 24
	errs := make(chan error, total)
	for index := 0; index < total; index++ {
		go func(index int) {
			var result int
			err := c.peer.Call(t.Context(), "host.echo", index, &result)
			if err == nil && result != index {
				err = fmt.Errorf("call %d received %d", index, result)
			}
			errs <- err
		}(index)
	}
	messages := make([]Message, total)
	seen := map[string]bool{}
	for index := range messages {
		messages[index] = c.read(t)
		id := string(messages[index].ID)
		if seen[id] || !strings.HasPrefix(id, `"ext-`) {
			t.Fatal("invalid/duplicate call id", id)
		}
		seen[id] = true
	}
	for index := total - 1; index >= 0; index-- {
		m := messages[index]
		c.write(t, Message{JSONRPC: "2.0", ID: m.ID, Result: m.Params})
	}
	for range total {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
func TestNotificationsRemainOrderedDuringHostCallback(t *testing.T) {
	var peer *Peer
	order := make(chan int, 3)
	finished := make(chan error, 1)
	c := connect(t, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		var index int
		_ = json.Unmarshal(params, &index)
		order <- index
		if index == 1 {
			var result string
			err := peer.Call(ctx, "session.snapshot", map[string]any{}, &result)
			if err == nil && result != "ready" {
				err = errors.New("callback result lost")
			}
			finished <- err
		}
		return nil, nil
	})
	peer = c.peer
	c.write(t, Message{JSONRPC: "2.0", Method: "lifecycle.event", Params: raw(1)})
	if first := <-order; first != 1 {
		t.Fatal(first)
	}
	callback := c.read(t)
	if callback.Method != "session.snapshot" {
		t.Fatal(callback)
	}
	c.write(t, Message{JSONRPC: "2.0", Method: "lifecycle.event", Params: raw(2)})
	c.write(t, Message{JSONRPC: "2.0", Method: "lifecycle.event", Params: raw(3)})
	select {
	case value := <-order:
		t.Fatal("notification passed blocked predecessor", value)
	default:
	}
	c.write(t, Message{JSONRPC: "2.0", ID: callback.ID, Result: raw("ready")})
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if second, third := <-order, <-order; second != 2 || third != 3 {
		t.Fatal(second, third)
	}
}
func TestCancellationTargetsExactRequestID(t *testing.T) {
	started := make(chan string, 2)
	c := connect(t, func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		id := RequestID(ctx)
		started <- id
		if method == "blocked" {
			<-ctx.Done()
			return nil, &Error{Code: -32800, Message: ctx.Err().Error()}
		}
		return "unrelated", nil
	})
	c.write(t, Message{JSONRPC: "2.0", ID: raw("turn-1"), Method: "blocked"})
	if id := <-started; id != "turn-1" {
		t.Fatal(id)
	}
	c.write(t, Message{JSONRPC: "2.0", ID: raw(7), Method: "other"})
	if id := <-started; id != "7" {
		t.Fatal(id)
	}
	other := c.read(t)
	if string(other.ID) != "7" || string(other.Result) != `"unrelated"` {
		t.Fatal(other)
	}
	c.write(t, Message{JSONRPC: "2.0", Method: "cancel", Params: raw(map[string]any{"id": "turn-1"})})
	cancelled := c.read(t)
	if string(cancelled.ID) != `"turn-1"` || cancelled.Error == nil || cancelled.Error.Code != -32800 {
		t.Fatal(cancelled)
	}
}
func TestEOFUnblocksCallsAndCancelsRequests(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	c := connect(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	c.write(t, Message{JSONRPC: "2.0", ID: raw(1), Method: "blocked"})
	<-started
	callDone := make(chan error, 1)
	go func() { callDone <- c.peer.Call(t.Context(), "host.wait", nil, nil) }()
	_ = c.read(t)
	c.input.Close()
	select {
	case err := <-callDone:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("call survived EOF")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request survived EOF")
	}
	reply := c.read(t)
	if string(reply.ID) != "1" || reply.Error == nil || reply.Error.Message != context.Canceled.Error() {
		t.Fatal("canceled reply lost at EOF", reply)
	}
	if err := <-c.done; err != nil {
		t.Fatal(err)
	}
}
func TestEOFDrainsPipedRequests(t *testing.T) {
	var input, output bytes.Buffer
	encoder := json.NewEncoder(&input)
	const count = 24
	for index := range count {
		if err := encoder.Encode(Message{JSONRPC: "2.0", ID: raw(index), Method: "initialize", Params: raw(index)}); err != nil {
			t.Fatal(err)
		}
	}
	peer := New(&output)
	if err := peer.Serve(t.Context(), &input, func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		return params, nil
	}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	seen := make(map[string]bool)
	for range count {
		var reply Message
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal("piped request reply lost", err)
		}
		if reply.Error != nil || string(reply.ID) != string(reply.Result) || seen[string(reply.ID)] {
			t.Fatal("incorrect piped request reply", reply)
		}
		seen[string(reply.ID)] = true
	}
	var extra Message
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatal("unexpected trailing reply", extra, err)
	}
}
func TestEOFDrainRemainsBounded(t *testing.T) {
	for _, blockedOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("blockedOutput=%v", blockedOutput), func(t *testing.T) {
			var output io.Writer = io.Discard
			release := make(chan struct{})
			finished := make(chan struct{})
			if blockedOutput {
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				output = writer
			}
			peer := New(output)
			input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"blocked"}` + "\n")
			started := time.Now()
			err := peer.Serve(t.Context(), input, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
				defer close(finished)
				if !blockedOutput {
					<-release // Deliberately ignore cancellation to exercise the shutdown budget.
				}
				return nil, ctx.Err()
			})
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
				t.Fatalf("EOF drain duration=%v", elapsed)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("test handler did not finish after release")
			}
		})
	}
}
func TestErrorsPreserveRPCCodeAndMarshalFailure(t *testing.T) {
	c := connect(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "specific":
			return nil, fmt.Errorf("wrapped: %w", &Error{Code: 422, Message: "upstream detail"})
		case "generic":
			return nil, errors.New("plain failure")
		case "unserializable":
			return make(chan int), nil
		}
		return nil, nil
	})
	for _, tc := range []struct {
		method string
		code   int
		detail string
	}{{"specific", 422, "upstream detail"}, {"generic", -32000, "plain failure"}, {"unserializable", -32000, "unsupported type"}} {
		c.write(t, Message{JSONRPC: "2.0", ID: raw(tc.method), Method: tc.method})
		m := c.read(t)
		if m.Error == nil || m.Error.Code != tc.code || !strings.Contains(m.Error.Message, tc.detail) {
			t.Fatal(m)
		}
	}
	returned := make(chan error, 1)
	go func() { returned <- c.peer.Call(t.Context(), "host.failure", nil, nil) }()
	outbound := c.read(t)
	c.write(t, Message{JSONRPC: "2.0", ID: outbound.ID, Error: &Error{Code: 409, Message: "host rejected"}})
	var rpcErr *Error
	if err := <-returned; !errors.As(err, &rpcErr) || rpcErr.Code != 409 {
		t.Fatal(err)
	}
}
func TestPreCancelledAndClosedCallsDoNotWrite(t *testing.T) {
	for _, closed := range []bool{false, true} {
		var out bytes.Buffer
		p := New(&out)
		ctx, cancel := context.WithCancel(t.Context())
		expected := error(context.Canceled)
		if closed {
			p.Close()
			expected = io.EOF
		} else {
			cancel()
		}
		err := p.Call(ctx, "must-not-run", map[string]any{}, nil)
		cancel()
		if !errors.Is(err, expected) || out.Len() != 0 || len(p.pending) != 0 {
			t.Fatalf("closed=%v error=%v bytes=%d pending=%d", closed, err, out.Len(), len(p.pending))
		}
	}
}
func TestCallsReturnOnContextCancellation(t *testing.T) {
	c := connect(t, func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { finished <- c.peer.Call(ctx, "host.wait", nil, nil) }()
	m := c.read(t)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("call ignored cancellation")
	}
	c.write(t, Message{JSONRPC: "2.0", ID: m.ID, Result: raw("late")})
	c.peer.mu.Lock()
	pending := len(c.peer.pending)
	c.peer.mu.Unlock()
	if pending != 0 {
		t.Fatal("late reply retained request", pending)
	}
}
func TestLargeResponseAndLineBudget(t *testing.T) {
	const payloadBytes = 64 * 1024 * 1024
	content := strings.Repeat("x", payloadBytes)
	var received int
	c := connect(t, func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var value string
		if err := json.Unmarshal(params, &value); err != nil {
			return nil, err
		}
		received = len(value)
		return received, nil
	})
	c.write(t, Message{JSONRPC: "2.0", ID: raw("large"), Method: "large", Params: raw(content)})
	response := c.readWithin(t, 30*time.Second)
	if string(response.Result) != fmt.Sprint(payloadBytes) {
		t.Fatal(response)
	}
	c.input.Close()
	if err := <-c.done; err != nil {
		t.Fatal(err)
	}
	if received != payloadBytes {
		t.Fatal(received)
	}
	p := New(io.Discard)
	err := p.Serve(t.Context(), strings.NewReader(strings.Repeat("x", MaxLineBytes+1)), func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("oversize record dispatched")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatal(err)
	}
}
func TestConcurrentNotificationsRemainCompleteJSON(t *testing.T) {
	var out bytes.Buffer
	peer := New(&out)
	var wg sync.WaitGroup
	for index := 0; index < 32; index++ {
		wg.Go(func() {
			if err := peer.Notify("progress", map[string]any{"value": strings.Repeat("x", 1024)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	decoder := json.NewDecoder(&out)
	for index := 0; index < 32; index++ {
		var m Message
		if err := decoder.Decode(&m); err != nil || m.Method != "progress" {
			t.Fatal(err, m)
		}
	}
	var trailing Message
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
