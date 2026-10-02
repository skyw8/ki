package main

import (
	"context"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func BenchmarkCodexRequestHistory(b *testing.B) {
	input := make([]any, 500)
	for i := range input {
		input[i] = object{"type": "message", "role": "user", "content": []any{object{"type": "input_text", "text": "history"}}}
	}
	body := object{"model": "gpt-5", "input": input}
	payload := object{"credential": object{"value": object{"access": "token", "accountId": "account"}}, "request": object{"sessionId": "benchmark"}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := codexRequest(context.Background(), payload, body, false); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCodexRequestDoesNotMutateBody(t *testing.T) {
	content := object{"type": "input_text", "text": "original"}
	body := object{"input": []any{object{"content": []any{content}}}, "client_metadata": object{"keep": true}}
	payload := object{"credential": object{"value": object{"access": "token", "accountId": "account"}}, "request": object{"sessionId": "session"}}
	req, err := codexRequest(context.Background(), payload, body, false)
	if err != nil {
		t.Fatal(err)
	}
	defer req.Body.Close()
	if content["text"] != "original" || obj(body["client_metadata"])["keep"] != true {
		t.Fatal("request construction mutated caller-owned input")
	}
	serialized, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Content-Encoding") != "zstd" {
		t.Fatal("Codex HTTP request is not zstd-compressed")
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	serialized, err = decoder.DecodeAll(serialized, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got object
	if err := decode(string(serialized), &got); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got["input"], body["input"])
	if obj(got["client_metadata"])["session_id"] != "session" {
		t.Fatal("request metadata missing")
	}
}
