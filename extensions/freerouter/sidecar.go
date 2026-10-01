package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

func extensionRootFromEnv() string {
	if root, ok := os.LookupEnv("KI_EXTENSION_ROOT"); ok {
		if absolute, err := filepath.Abs(root); err == nil {
			return absolute
		}
		return root
	}
	root, err := os.Getwd()
	if err != nil {
		return "."
	}
	return root
}

type rpcWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (w *rpcWriter) write(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.encoder.Encode(value)
}
func (w *rpcWriter) respond(id json.RawMessage, result any) error {
	if len(id) == 0 {
		return nil
	}
	return w.write(object{"jsonrpc": "2.0", "id": id, "result": result})
}

type sidecarRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  object          `json:"params"`
}

func runSidecar(ctx context.Context, p *pool, root string, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := &rpcWriter{encoder: json.NewEncoder(out)}
	var mu sync.Mutex
	var wg sync.WaitGroup
	cancels := map[string]context.CancelFunc{}
	defer func() { cancel(); wg.Wait() }()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 65*1024*1024)
	for scanner.Scan() {
		var msg sidecarRequest
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.JSONRPC != "2.0" || msg.Method == "" {
			continue
		}
		params := msg.Params
		switch msg.Method {
		case "initialize":
			if err := writer.respond(msg.ID, object{"tools": []any{}, "commands": []any{}, "fallback": false, "subscriptions": []any{}}); err != nil {
				return err
			}
			// Explicit success keeps the provider status consistent with ready
			// extensions; an info tone would render the ready chip as inactive gray.
			if err := writer.write(object{"jsonrpc": "2.0", "id": fmt.Sprintf("freerouter-status-%d", os.Getpid()), "method": "ui.setGlobalStatus", "params": object{"text": "freerouter", "tone": "success"}}); err != nil {
				return err
			}
		case "provider.stream.start":
			rid := str(params["requestId"])
			if err := writer.respond(msg.ID, object{"accepted": true}); err != nil {
				return err
			}
			streamCtx, stop := context.WithCancel(ctx)
			mu.Lock()
			if prior := cancels[rid]; prior != nil {
				prior()
			}
			cancels[rid] = stop
			mu.Unlock()
			wg.Add(1)
			go func(params object, rid string) {
				defer wg.Done()
				defer stop()
				wrapper := obj(params["request"])
				credential := str(obj(wrapper["credential"])["apiKey"])
				key := p.resolveKey(credential)
				request := obj(wrapper["request"])
				if request == nil {
					request = wrapper
				}
				runRace(streamCtx, p, kiRequestToChat(request), key, func(ev object) {
					ev["requestId"] = rid
					if writer.write(object{"jsonrpc": "2.0", "method": "provider.stream.event", "params": ev}) != nil {
						stop()
					}
				}, true)
				// Do not remove a replacement stream's cancel function when a duplicate
				// request ID supersedes this stream while it is completing.
				mu.Lock()
				if streamCtx.Err() == nil {
					delete(cancels, rid)
				}
				mu.Unlock()
			}(params, rid)
		case "provider.stream.cancel":
			rid := str(params["requestId"])
			mu.Lock()
			stop := cancels[rid]
			delete(cancels, rid)
			mu.Unlock()
			if stop != nil {
				stop()
			}
			if err := writer.respond(msg.ID, object{}); err != nil {
				return err
			}
		case "cancel":
			if err := writer.respond(msg.ID, object{}); err != nil {
				return err
			}
		case "config.updated":
			p.updateConfig(loadSidecar(root))
			if err := writer.respond(msg.ID, object{}); err != nil {
				return err
			}
		case "shutdown":
			return writer.respond(msg.ID, object{})
		default:
			if len(msg.ID) > 0 {
				if err := writer.write(object{"jsonrpc": "2.0", "id": msg.ID, "error": object{"code": -32601, "message": "method not found: " + msg.Method}}); err != nil {
					return err
				}
			}
		}
	}
	return scanner.Err()
}
