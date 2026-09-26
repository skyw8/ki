package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ki/internal/loop"
	"ki/internal/tools"
	"ki/internal/types"
)

// probeStreamer asks the Bash tool to run one command and then finishes, so a
// test can observe what the shell tool actually resolved.
type probeStreamer struct {
	command string
}

func (s *probeStreamer) Stream(_ context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	for _, msg := range req.Messages {
		if msg.Role == "toolResult" {
			return types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "probe done"}}, StopReason: "stop"}, nil
		}
	}
	return types.Message{Role: "assistant", Content: []types.Content{{
		Type: "toolCall", ID: "path-probe", Name: "Bash",
		Arguments: map[string]any{"command": s.command},
	}}, StopReason: "toolUse"}, nil
}

func writePathExtension(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","capabilities":["path"],"runtime":{"kind":"none","path":["bin"]}}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "ziprobe"), []byte("#!/bin/sh\necho extension-path-marker\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// runProbe prompts the session and returns the persisted transcript, which
// contains the Bash tool result.
func runProbe(t *testing.T, hs *httptest.Server, id string) string {
	t.Helper()
	body, _ := marshalJSON(map[string]any{"text": "run the probe"})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("prompt status = %d", res.StatusCode)
	}
	waitAgentEnd(t, hs, id)

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return sessionBlob(got)
}

func disableExtension(t *testing.T, hs *httptest.Server, name string) {
	t.Helper()
	body, _ := marshalJSON(map[string]any{"disabled": []string{name}})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, hs.URL+"/v1/extensions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("disable %s status = %d", name, res.StatusCode)
	}
}

// TestExtensionPathDirsReachShellTools drives the whole chain: manifest path
// capability -> resource snapshot -> tools.Set -> shell child PATH. The probe
// script is a POSIX shell script, so the test is POSIX-only.
func TestExtensionPathDirsReachShellTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probe script is a POSIX shell script")
	}
	if !tools.DiscoverShellRuntime().BashAvailable() {
		t.Skip("bash unavailable")
	}
	srv, hs := testServerWith(t, &probeStreamer{command: "ziprobe"})
	writePathExtension(t, srv.cfg.Home, "pathprobe")

	id := createSession(t, hs, t.TempDir())
	if blob := runProbe(t, hs, id); !strings.Contains(blob, "extension-path-marker") {
		t.Fatalf("extension CLI was not on the shell PATH:\n%s", blob)
	}

	// Disabling the package must drop the directory: the toggle reloads
	// resources, so a session loaded afterwards sees no PATH entry and the
	// command is not found.
	disableExtension(t, hs, "pathprobe")
	disabledID := createSession(t, hs, t.TempDir())
	blob := runProbe(t, hs, disabledID)
	if strings.Contains(blob, "extension-path-marker") || !strings.Contains(blob, "not found") {
		t.Fatalf("disabled package still resolved its CLI:\n%s", blob)
	}
}
