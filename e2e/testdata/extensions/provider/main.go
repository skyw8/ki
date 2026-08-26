package main

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type streamStart struct {
	RequestID string `json:"requestId"`
	Request   struct {
		Model struct {
			ID string `json:"id"`
		} `json:"model"`
	} `json:"request"`
}

type streamCancel struct {
	RequestID string `json:"requestId"`
}

type authRequest struct {
	RequestID string `json:"requestId"`
	Provider  string `json:"provider"`
}

var (
	writeMu sync.Mutex
	stops   = map[string]chan struct{}{}
	stopMu  sync.Mutex
)

func send(value any) {
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = json.NewEncoder(os.Stdout).Encode(value)
}

func reply(id any, result any) {
	send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func event(requestID, typ, delta string) {
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "provider.stream.event",
		"params": map[string]any{
			"requestId": requestID, "type": typ, "delta": delta, "contentIndex": 0,
		},
	})
}

func done(requestID, model, text string) {
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "provider.stream.event",
		"params": map[string]any{
			"requestId": requestID,
			"type":      "done",
			"message": map[string]any{
				"role": "assistant", "api": "fake-api", "provider": "fake-provider", "model": model,
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		},
	})
}

func authComplete(requestID, provider string) {
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "provider.auth.event",
		"params": map[string]any{
			"requestId": requestID, "provider": provider, "type": "completed",
			"credential": map[string]any{"type": "oauth", "value": map[string]any{"access": "fake-access", "refresh": "fake-refresh"}},
		},
	})
}

func isStopped(requestID string) bool {
	stopMu.Lock()
	stop := stops[requestID]
	stopMu.Unlock()
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func stream(start streamStart) {
	requestID, model := start.RequestID, start.Request.Model.ID
	text := "hello " + model
	event(requestID, "text_start", "")
	event(requestID, "text_delta", "hello ")
	if model == "slow" {
		for i := 0; i < 100; i++ {
			if isStopped(requestID) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if isStopped(requestID) {
		return
	}
	event(requestID, "text_delta", model)
	event(requestID, "text_end", "")
	done(requestID, model, text)
	stopMu.Lock()
	delete(stops, requestID)
	stopMu.Unlock()
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var msg message
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			if marker := os.Getenv("KI_MARKER"); marker != "" {
				_ = os.WriteFile(marker, []byte("1"), 0o600)
			}
			reply(msg.ID, map[string]any{"tools": []any{}, "commands": []any{}})
		case "provider.stream.start":
			var start streamStart
			if json.Unmarshal(msg.Params, &start) != nil || start.RequestID == "" {
				reply(msg.ID, map[string]any{"accepted": false})
				continue
			}
			stopMu.Lock()
			stops[start.RequestID] = make(chan struct{})
			stopMu.Unlock()
			reply(msg.ID, map[string]any{"accepted": true})
			go stream(start)
		case "provider.auth.start":
			var auth authRequest
			if json.Unmarshal(msg.Params, &auth) != nil || auth.RequestID == "" {
				reply(msg.ID, map[string]any{"accepted": false})
				continue
			}
			reply(msg.ID, map[string]any{"accepted": true})
			go authComplete(auth.RequestID, auth.Provider)
		case "provider.auth.input":
			reply(msg.ID, map[string]any{"accepted": true})
		case "provider.auth.refresh":
			reply(msg.ID, map[string]any{"refreshed": false})
		case "provider.stream.cancel":
			var cancel streamCancel
			_ = json.Unmarshal(msg.Params, &cancel)
			stopMu.Lock()
			if stop := stops[cancel.RequestID]; stop != nil {
				select {
				case <-stop:
				default:
					close(stop)
				}
			}
			stopMu.Unlock()
		default:
			if msg.ID != nil {
				reply(msg.ID, map[string]any{})
			}
		}
	}
}
