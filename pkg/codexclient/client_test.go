package codexclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"ki/internal/state"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("KI_HOME", t.TempDir())
	sessions.Lock()
	clear(sessions.values)
	sessions.Unlock()
}

func prepare(t *testing.T, session, turn string, compact bool) (map[string]any, map[string]string) {
	t.Helper()
	body := map[string]any{"model": "gpt-6-astra", "stream": true, "input": []any{}}
	headers, err := Prepare(body, session, turn, compact)
	if err != nil {
		t.Fatal(err)
	}
	return body, headers
}

func TestPersistentIdentityAndConcurrentCreation(t *testing.T) {
	isolate(t)
	const n = 16
	results := make(chan string, n)
	errors := make(chan error, n)
	var workers sync.WaitGroup
	for range n {
		workers.Go(func() {
			body := map[string]any{"model": "gpt-6-astra"}
			_, err := Prepare(body, "", "", false)
			if err != nil {
				errors <- err
				return
			}
			results <- body["client_metadata"].(map[string]any)["x-codex-installation-id"].(string)
		})
	}
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	id := ""
	for result := range results {
		if id != "" && result != id {
			t.Fatalf("identity raced: %s vs %s", id, result)
		}
		id = result
	}
	raw, _, err := state.ReadFile(filepath.Join(os.Getenv("KI_HOME"), "codex-client.json"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	var d identityDocument
	if err := json.Unmarshal(raw, &d); err != nil || d.Version != 1 || d.InstallationID != id {
		t.Fatalf("identity document: %s %v", raw, err)
	}
	if len(sessions.values) != 0 {
		t.Fatal("ephemeral requests retained session state")
	}
}

func TestNewerIdentityIsNotRewritten(t *testing.T) {
	isolate(t)
	path := filepath.Join(os.Getenv("KI_HOME"), "codex-client.json")
	if err := state.WriteJSON(path, map[string]any{"version": 2, "installation_id": "future", "unknown": true}, 0o600); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"model": "gpt-6-astra"}
	if _, err := Prepare(body, "session", "turn", false); !errors.Is(err, state.ErrNewerVersion) {
		t.Fatal(err)
	}
	raw, _, err := state.ReadFile(path, 2, nil)
	if err != nil || !bytes.Contains(raw, []byte(`"unknown": true`)) {
		t.Fatalf("newer identity modified: %s %v", raw, err)
	}
}

func TestMalformedIdentityFailsClosed(t *testing.T) {
	isolate(t)
	path := filepath.Join(os.Getenv("KI_HOME"), "codex-client.json")
	id := strings.Repeat("x", 36)
	if err := state.WriteJSON(path, identityDocument{1, id}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(map[string]any{"model": "test"}, "session", "turn", false); err == nil {
		t.Fatal("invalid UUID accepted")
	}
	raw, _, err := state.ReadFile(path, 1, nil)
	if err != nil || !bytes.Contains(raw, []byte(id)) {
		t.Fatal("malformed identity overwritten", err)
	}
}

func TestBindingAndConcurrentTurnIsolation(t *testing.T) {
	isolate(t)
	makeRequest := func(binding, turn string) map[string]string {
		headers, err := PrepareScoped(map[string]any{"model": "test"}, "session", turn, false, binding)
		if err != nil {
			t.Fatal(err)
		}
		return headers
	}
	first := makeRequest("backend-account-a", "turn-1")
	makeRequest("backend-account-a", "turn-2")
	CaptureTurnStateScoped("session", "turn-1", "first-token", "backend-account-a")
	CaptureTurnStateScoped("session", "turn-2", "second-token", "backend-account-a")
	secondBinding := makeRequest("backend-account-b", "turn-1")
	if secondBinding["x-codex-turn-state"] != "" || secondBinding["x-codex-window-id"] == first["x-codex-window-id"] {
		t.Fatal("cross-account state reused", secondBinding)
	}
	if got := makeRequest("backend-account-a", "turn-1")["x-codex-turn-state"]; got != "first-token" {
		t.Fatal("second turn reset active turn", got)
	}
	if got := makeRequest("backend-account-a", "turn-2")["x-codex-turn-state"]; got != "second-token" {
		t.Fatal("active turn reset successor", got)
	}
}

func TestRoutingTurnAndWindowLifecycle(t *testing.T) {
	isolate(t)
	body, first := prepare(t, "session", "turn-1", false)
	metadata := body["client_metadata"].(map[string]any)
	var turn map[string]any
	if err := json.Unmarshal([]byte(first["x-codex-turn-metadata"]), &turn); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"session_id": "session", "thread_id": "session", "turn_id": "turn-1", "root_turn_id": "turn-1",
		"window_id": first["x-codex-window-id"], "request_kind": "turn",
		"installation_id": metadata["x-codex-installation-id"],
	} {
		if turn[key] != value {
			t.Fatalf("%s: %v != %v", key, turn[key], value)
		}
	}
	if first["OpenAI-Beta"] != "" || first["x-codex-routing-hint"] != "model=gpt-6-astra" {
		t.Fatal(first)
	}
	body["service_tier"] = "priority"
	fast, err := Prepare(body, "session", "turn-1", false)
	if err != nil || fast["x-codex-routing-hint"] != "model=gpt-6-astra;tier=priority" {
		t.Fatal(fast, err)
	}
	CaptureTurnState("session", "turn-1", "sticky-first")
	CaptureTurnState("session", "turn-1", "sticky-second")
	_, next := prepare(t, "session", "turn-1", false)
	if next["x-codex-turn-state"] != "sticky-first" || next["x-codex-window-id"] != first["x-codex-window-id"] {
		t.Fatal(next)
	}
	_, newer := prepare(t, "session", "turn-2", false)
	CaptureTurnState("session", "turn-1", "stale")
	_, newer = prepare(t, "session", "turn-2", false)
	if newer["x-codex-turn-state"] != "" || newer["x-codex-window-id"] != first["x-codex-window-id"] {
		t.Fatal(newer)
	}
	AdvanceWindow("session")
	compactBody, compact := prepare(t, "session", "turn-2", true)
	if compact["x-codex-window-id"] == first["x-codex-window-id"] {
		t.Fatal("checkpoint did not advance window")
	}
	var checkpoint map[string]any
	_ = json.Unmarshal([]byte(compactBody["client_metadata"].(map[string]any)["x-codex-turn-metadata"].(string)), &checkpoint)
	descriptor := checkpoint["compaction"].(map[string]any)
	if descriptor["trigger"] != nil || descriptor["reason"] != nil || descriptor["implementation"] != "responses_compaction_v2" {
		t.Fatal(descriptor)
	}
}

func TestEncodeCompressionAndSanitizedAgent(t *testing.T) {
	isolate(t)
	t.Setenv("TERM_PROGRAM", "test\nterminal\u2603")
	body := map[string]any{"input": []any{"large history"}, "model": "gpt-6-astra", "stream": true, "service_tier": "priority"}
	before, _ := json.Marshal(body)
	raw, err := Encode(body)
	if err != nil || !strings.HasPrefix(string(raw), `{"model":"gpt-6-astra","stream":true,"service_tier":"priority",`) {
		t.Fatalf("routing order: %s %v", raw, err)
	}
	after, _ := json.Marshal(body)
	if !bytes.Equal(before, after) {
		t.Fatal("Encode mutated body")
	}
	compressed, err := Compress(raw)
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	decoded, err := decoder.DecodeAll(compressed, nil)
	if err != nil || !bytes.Equal(decoded, raw) {
		t.Fatal(err, string(decoded))
	}
	if ua := UserAgent(); strings.ContainsAny(ua, "\n\u2603") || !strings.HasPrefix(ua, "codex_cli_rs/0.0.0 (") {
		t.Fatal(ua)
	}
}
