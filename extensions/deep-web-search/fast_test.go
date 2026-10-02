package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ki/internal/state"
)

func TestCodexDirectFastThinking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	credential := obj{"providers": obj{"openai-codex": obj{"type": "oauth", "value": obj{"access": "test-access", "accountId": "test-account", "expires": time.Now().Add(time.Hour).UnixMilli()}}}}
	if err := state.WriteVersioned(filepath.Join(home, "credentials.json"), 1, credential, 0600); err != nil {
		t.Fatal(err)
	}
	type captured struct {
		body    obj
		headers http.Header
		wire    []byte
	}
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body obj
		raw, err := io.ReadAll(r.Body)
		wire := bytes.Clone(raw)
		if err == nil {
			raw, err = decodeCodexTestBody(raw)
		}
		if err == nil {
			err = json.Unmarshal(raw, &body)
		}
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- captured{body, r.Header.Clone(), wire}
		fmt.Fprint(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
	}))
	defer server.Close()
	t.Setenv("KI_DEEP_WEB_SEARCH_CODEX_URL", server.URL)
	for _, base := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		for _, fast := range []bool{false, true} {
			level := base
			if fast {
				level += " fast"
			}
			for _, operation := range []string{"search", "summary"} {
				t.Run(operation+"/"+level, func(t *testing.T) {
					if operation == "search" {
						if _, err := searchCodex(t.Context(), "query", obj{"numResults": 5}, obj{"codexModel": "test-model", "codexThinkingEffort": level}); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, _, err := completeWithModel(t.Context(), "prompt", "openai-codex/test-model", level, time.Second); err != nil {
							t.Fatal(err)
						}
					}
					got := <-requests
					if got.headers.Get("Content-Encoding") != "zstd" || bytes.HasPrefix(got.wire, []byte("{")) {
						t.Fatalf("Codex body was not sent as compressed zstd: %v", got.headers)
					}
					if base == "off" {
						if _, exists := got.body["reasoning"]; exists {
							t.Fatal("off fast must not enable reasoning")
						}
					} else if record(got.body["reasoning"])["effort"] != base {
						t.Fatalf("reasoning effort: %v", got.body["reasoning"])
					}
					hint := "model=test-model"
					if fast {
						hint += ";tier=priority"
						if got.body["service_tier"] != "priority" {
							t.Fatal("missing priority tier")
						}
					} else if _, exists := got.body["service_tier"]; exists {
						t.Fatal("normal thinking must omit tier")
					}
					if got.headers.Get("x-codex-routing-hint") != hint || got.headers.Get("originator") != "codex_cli_rs" || !strings.HasPrefix(got.headers.Get("User-Agent"), "codex_cli_rs/") {
						t.Fatalf("Codex identity/routing headers: %v", got.headers)
					}
					if got.headers.Get("Authorization") != "Bearer test-access" || got.headers.Get("chatgpt-account-id") != "test-account" {
						t.Fatal("lost authentication headers")
					}
					if got.headers.Get("OpenAI-Beta") != "" || got.headers.Get("Accept") != "text/event-stream" {
						t.Fatalf("HTTP stream headers: %v", got.headers)
					}
					metadata := record(got.body["client_metadata"])
					if str(metadata["session_id"]) == "" || str(metadata["turn_id"]) == "" || str(metadata["x-codex-installation-id"]) == "" {
						t.Fatalf("missing Codex metadata: %v", metadata)
					}
					if (operation == "search") != (got.body["tools"] != nil) {
						t.Fatal("search tools leaked into summary")
					}
				})
			}
		}
	}
}

func TestOpenAIDirectSummaryRejectsFastBeforeAuthOrNetwork(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	for _, base := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		t.Run(base, func(t *testing.T) {
			_, _, err := completeWithModel(t.Context(), "prompt", "openai/test-model", base+" fast", time.Second)
			if err == nil || !strings.Contains(err.Error(), "Fast thinking requires a Codex provider") {
				t.Fatalf("must reject Fast before credentials or network: %v", err)
			}
		})
	}
}

func TestOpenAIDirectSummaryKeepsJSONWire(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-openai-key")
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Header.Get("Content-Encoding") != "" || r.Header.Get("x-codex-routing-hint") != "" {
			t.Errorf("Codex compression or routing leaked into OpenAI request: %v", r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer test-openai-key" {
			t.Error("lost OpenAI credentials")
		}
		requests <- raw
		fmt.Fprint(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
	}))
	defer server.Close()
	t.Setenv("KI_DEEP_WEB_SEARCH_OPENAI_URL", server.URL)
	if _, _, err := completeWithModel(t.Context(), "prompt", "openai/test-model", "high", time.Second); err != nil {
		t.Fatal(err)
	}
	raw := <-requests
	var body obj
	if !bytes.HasPrefix(raw, []byte("{")) || json.Unmarshal(raw, &body) != nil {
		t.Fatalf("OpenAI request must remain JSON: %q", raw)
	}
	if record(body["reasoning"])["effort"] != "high" || body["service_tier"] != nil || body["client_metadata"] != nil {
		t.Fatalf("unexpected OpenAI request: %v", body)
	}
}
