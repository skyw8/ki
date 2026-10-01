package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Vary", "Accept-Encoding")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(status)
		z := gzip.NewWriter(w)
		_ = json.NewEncoder(z).Encode(value)
		_ = z.Close()
		return
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func httpError(w http.ResponseWriter, r *http.Request, status int, message, typ string) {
	writeJSON(w, r, status, object{"error": object{"message": message, "type": typ}})
}
func httpRouter(p *pool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, r, http.StatusOK, object{"ok": true}) })
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		data := []any{object{"id": "auto", "object": "model", "owned_by": "freerouter"}}
		for _, m := range p.discovery.cached() {
			data = append(data, object{"id": m.ID, "object": "model", "owned_by": "openrouter-free", "name": m.Name})
		}
		writeJSON(w, r, http.StatusOK, object{"object": "list", "data": data})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { chatCompletions(p, w, r) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type chatRequest struct {
	Model     *string `json:"model"`
	Messages  []any   `json:"messages"`
	Tools     []any   `json:"tools"`
	MaxTokens uint64  `json:"max_tokens"`
	Stream    bool    `json:"stream"`
}

func chatCompletions(p *pool, w http.ResponseWriter, r *http.Request) {
	var body chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024)).Decode(&body); err != nil {
		httpError(w, r, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if body.Messages == nil {
		httpError(w, r, http.StatusUnprocessableEntity, "messages is required", "invalid_request_error")
		return
	}
	key := p.resolveKey("")
	if key == "" {
		httpError(w, r, http.StatusUnauthorized, "No OpenRouter API key configured", "authentication_error")
		return
	}
	model := "auto"
	if body.Model != nil {
		model = *body.Model
	}
	pinned := ""
	switch model {
	case "", "auto", "free-router/auto", "freerouter/auto":
	default:
		if strings.Contains(model, ":free") {
			pinned = model
		} else {
			httpError(w, r, http.StatusBadRequest, fmt.Sprintf("Unknown model '%s'. Use 'auto' or a :free model id.", model), "invalid_request_error")
			return
		}
	}
	input := raceInput{Messages: body.Messages, Tools: body.Tools, MaxTokens: body.MaxTokens, PinnedModel: pinned}
	if body.Stream {
		serveSSE(r.Context(), p, w, input, key)
		return
	}
	var final object
	var failure object
	runRace(r.Context(), p, input, key, func(ev object) {
		switch str(ev["type"]) {
		case "done":
			final = obj(ev["message"])
		case "error":
			failure = ev
		}
	}, false)
	if failure != nil {
		message := str(failure["error"])
		status := http.StatusBadGateway
		if str(failure["reason"]) == "aborted" {
			status = http.StatusBadRequest
		} else if strings.Contains(message, "exhausted") {
			status = http.StatusServiceUnavailable
		}
		typ := "freerouter_error"
		if status == http.StatusServiceUnavailable {
			typ = "freerouter_exhausted"
		}
		httpError(w, r, status, message, typ)
		return
	}
	if final == nil {
		httpError(w, r, http.StatusBadGateway, "empty response", "freerouter_error")
		return
	}
	modelID := str(final["model"])
	if modelID == "" {
		modelID = "auto"
	}
	w.Header().Set("X-Freerouter-Model", modelID)
	writeJSON(w, r, http.StatusOK, assistantToChatCompletion(final, modelID))
}
func serveSSE(ctx context.Context, p *pool, w http.ResponseWriter, input raceInput, key string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
	events := make(chan object, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRace(ctx, p, input, key, func(ev object) {
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}, false)
	}()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	send := func(ev object) bool {
		value := sseValue(ev)
		if value == nil {
			return true
		}
		_, err := fmt.Fprintf(w, "data: %s\n\n", jsonText(value))
		flusher.Flush()
		return err == nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			if !send(ev) {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-done:
			// The run can finish before the handler drains its final queued event.
			// Drain before [DONE] to preserve the OpenAI stream's terminal ordering.
			for {
				select {
				case ev := <-events:
					if !send(ev) {
						return
					}
				default:
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
					flusher.Flush()
					return
				}
			}
		}
	}
}
func finishReason(message object) string {
	switch str(message["stopReason"]) {
	case "toolUse":
		return "tool_calls"
	case "length":
		return "length"
	}
	return "stop"
}
func chatChunk(model string, delta object, finish any) object {
	if model == "" {
		model = "auto"
	}
	if delta == nil {
		delta = object{}
	}
	return object{"id": fmt.Sprintf("chatcmpl-fr-%d", nowMS()), "object": "chat.completion.chunk", "created": nowMS() / 1000, "model": model, "choices": []any{object{"index": 0, "delta": delta, "finish_reason": finish}}}
}
func sseValue(ev object) object {
	switch str(ev["type"]) {
	case "text_delta":
		return chatChunk("", object{"content": ev["delta"]}, nil)
	case "toolcall_start":
		return chatChunk("", object{"tool_calls": []any{object{"index": 0, "id": ev["toolCallId"], "type": "function", "function": object{"name": ev["toolName"], "arguments": ""}}}}, nil)
	case "toolcall_delta":
		return chatChunk("", object{"tool_calls": []any{object{"index": 0, "function": object{"arguments": ev["delta"]}}}}, nil)
	case "done":
		message := obj(ev["message"])
		return chatChunk(str(message["model"]), object{}, finishReason(message))
	case "error":
		return object{"error": object{"message": ev["error"], "type": "freerouter_error"}}
	}
	return nil
}
func assistantToChatCompletion(message object, modelID string) object {
	var text strings.Builder
	calls := []any{}
	for _, v := range arr(message["content"]) {
		c := obj(v)
		switch str(c["type"]) {
		case "text":
			text.WriteString(str(c["text"]))
		case "toolCall":
			calls = append(calls, object{"id": or(c["id"], ""), "type": "function", "function": object{"name": or(c["name"], ""), "arguments": jsonText(or(c["arguments"], object{}))}})
		}
	}
	msg := object{"role": "assistant", "content": text.String()}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
		if text.Len() == 0 {
			msg["content"] = nil
		}
	}
	usage := obj(message["usage"])
	return object{"id": fmt.Sprintf("chatcmpl-fr-%d", nowMS()), "object": "chat.completion", "created": nowMS() / 1000, "model": modelID, "choices": []any{object{"index": 0, "message": msg, "finish_reason": finishReason(message)}}, "usage": object{"prompt_tokens": or(usage["input"], 0), "completion_tokens": or(usage["output"], 0), "total_tokens": or(usage["totalTokens"], 0)}}
}
func startHTTP(p *pool, listen string) (*http.Server, net.Listener, error) {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to bind %s: %w (set --listen / FREEROUTER_LISTEN / config listen)", listen, err)
	}
	server := &http.Server{Handler: httpRouter(p), ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(logOutput, "[freerouter] HTTP listening on http://%s\n", listener.Addr())
	return server, listener, nil
}
