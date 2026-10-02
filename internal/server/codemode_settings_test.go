package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/state"
	"ki/internal/types"
)

type codeModeSettingsItem struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
}

type codeModeSettingsCatalog struct {
	CodeMode string                 `json:"codeMode"`
	Items    []codeModeSettingsItem `json:"items"`
}

func codeModeSettingsHTTP(ctx context.Context, hs *httptest.Server, method, route, body string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, hs.URL+route, bytes.NewBufferString(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, raw, err
}

func codeModeSettingsRequest(t *testing.T, hs *httptest.Server, method, body string, wantStatus int) codeModeSettingsCatalog {
	t.Helper()
	status, raw, err := codeModeSettingsHTTP(t.Context(), hs, method, "/v1/tools", body)
	if err != nil {
		t.Fatal(err)
	}
	if status != wantStatus {
		t.Fatalf("%s tools status=%d want=%d body=%s", method, status, wantStatus, raw)
	}
	var got codeModeSettingsCatalog
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func codeModeSettingsAssertCatalog(t *testing.T, got codeModeSettingsCatalog, mode string, disabled ...string) {
	t.Helper()
	if got.CodeMode != mode {
		t.Fatalf("codeMode=%q want=%q", got.CodeMode, mode)
	}
	for _, name := range []string{"read", "exec", "wait"} {
		i := slices.IndexFunc(got.Items, func(item codeModeSettingsItem) bool {
			return item.Name == name
		})
		if i < 0 {
			t.Fatalf("catalog missing %s: %+v", name, got.Items)
		}
		item := got.Items[i]
		if item.Enabled != !slices.Contains(disabled, name) {
			t.Fatalf("%s enabled=%v disabled=%v", name, item.Enabled, disabled)
		}
		if (name == "exec" || name == "wait") && item.Available != (mode != "off") {
			t.Fatalf("%s available=%v mode=%s", name, item.Available, mode)
		}
	}
}

func codeModeSettingsPrompt(t *testing.T, hs *httptest.Server, id string) {
	t.Helper()
	status, raw, err := codeModeSettingsHTTP(t.Context(), hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", `{"text":"inspect tool schemas","model":"openai/gpt-5.6-terra"}`)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusAccepted {
		t.Fatalf("prompt status=%d body=%s", status, raw)
	}
	waitAgentEnd(t, hs, id)
}

func codeModeSettingsAssertTools(t *testing.T, req loop.Request, mode string) {
	t.Helper()
	names := requestToolNames(req.Tools)
	if mode == "only" {
		if !slices.Equal(names, []string{"exec", "wait"}) {
			t.Fatalf("only tools=%v", names)
		}
		return
	}
	if !slices.Contains(names, "read") {
		t.Fatalf("%s lost direct read: %v", mode, names)
	}
	for _, name := range []string{"exec", "wait"} {
		if slices.Contains(names, name) != (mode == "mixed") {
			t.Fatalf("%s tools=%v", mode, names)
		}
	}
}

func TestCodeModeSettingsDefaultAndNextPrompt(t *testing.T) {
	recorder := &reqRecorder{}
	_, hs := testServerWith(t, recorder)
	id := createSession(t, hs, t.TempDir())
	codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, hs, http.MethodGet, "", http.StatusOK), "mixed")
	for _, mode := range []string{"mixed", "only", "off", "mixed"} {
		if mode != "mixed" || len(recorder.reqs) > 0 {
			got := codeModeSettingsRequest(t, hs, http.MethodPatch, fmt.Sprintf(`{"codeMode":%q}`, mode), http.StatusOK)
			codeModeSettingsAssertCatalog(t, got, mode)
		}
		codeModeSettingsPrompt(t, hs, id)
		codeModeSettingsAssertTools(t, recorder.reqs[len(recorder.reqs)-1], mode)
	}
}

func TestCodeModeSettingsOmittedFieldsPreserved(t *testing.T) {
	_, hs := testServerWith(t, &reqRecorder{})
	got := codeModeSettingsRequest(t, hs, http.MethodPatch, `{"disabled":["read"],"codeMode":"only"}`, http.StatusOK)
	codeModeSettingsAssertCatalog(t, got, "only", "read")
	got = codeModeSettingsRequest(t, hs, http.MethodPatch, `{"codeMode":"off"}`, http.StatusOK)
	codeModeSettingsAssertCatalog(t, got, "off", "read")
	got = codeModeSettingsRequest(t, hs, http.MethodPatch, `{"disabled":[]}`, http.StatusOK)
	codeModeSettingsAssertCatalog(t, got, "off")
	got = codeModeSettingsRequest(t, hs, http.MethodPatch, `{}`, http.StatusOK)
	codeModeSettingsAssertCatalog(t, got, "off")
}

func codeModeSettingsSaved(t *testing.T, home string, version int) []byte {
	t.Helper()
	raw, _, err := state.ReadFile(filepath.Join(home, "toggles.json"), version, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCodeModeSettingsInvalidNoMutation(t *testing.T) {
	srv, hs := testServerWith(t, &reqRecorder{})
	codeModeSettingsRequest(t, hs, http.MethodPatch, `{"disabled":["read"],"codeMode":"only"}`, http.StatusOK)
	before := codeModeSettingsSaved(t, srv.cfg.Home, 2)
	for _, body := range []string{
		`{"codeMode":"invalid","disabled":[]}`, `{"codeMode":"","disabled":[]}`,
		`{"codeMode":null,"disabled":[]}`, `{"codeMode":7,"disabled":[]}`,
		`{"codeMode":"off"`, `{"codeMode":"off"} {}`, `null`, ``,
		`{"codeMode":"off","disabled":7}`,
		`{"codeMode":"off","disabled":["not-a-tool"]}`,
	} {
		t.Run(body, func(t *testing.T) {
			codeModeSettingsRequest(t, hs, http.MethodPatch, body, http.StatusBadRequest)
			after := codeModeSettingsSaved(t, srv.cfg.Home, 2)
			if !bytes.Equal(before, after) {
				t.Fatalf("invalid patch mutated toggles: before=%s after=%s", before, after)
			}
			codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, hs, http.MethodGet, "", http.StatusOK), "only", "read")
		})
	}
}

func TestCodeModeSettingsTOMLFallbackAndPersistedOverride(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("KI_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "ki.toml"), []byte("[code_mode]\nmode = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	newServer := func() (*Server, *httptest.Server) {
		t.Helper()
		srv, err := New(Options{Config: cfg, Token: "tok", Streamer: &reqRecorder{}})
		if err != nil {
			t.Fatal(err)
		}
		hs := httptest.NewServer(srv.Handler())
		t.Cleanup(hs.Close)
		t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
		return srv, hs
	}
	srv, hs := newServer()
	codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, hs, http.MethodGet, "", http.StatusOK), "off")
	codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, hs, http.MethodPatch, `{"codeMode":"only"}`, http.StatusOK), "only")
	raw := codeModeSettingsSaved(t, home, 2)
	var saved struct {
		Version  int `json:"version"`
		CodeMode struct {
			Mode string `json:"mode"`
		} `json:"code_mode"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 2 || saved.CodeMode.Mode != "only" {
		t.Fatalf("saved override=%s", raw)
	}
	hs.Close()
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, restored := newServer()
	codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, restored, http.MethodGet, "", http.StatusOK), "only")
}

func TestCodeModeSettingsConcurrentIndependentPatches(t *testing.T) {
	_, hs := testServerWith(t, &reqRecorder{})
	for round := range 8 {
		codeModeSettingsRequest(t, hs, http.MethodPatch, `{"codeMode":"mixed","disabled":[]}`, http.StatusOK)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, body := range []string{`{"codeMode":"only"}`, `{"disabled":["read"]}`} {
			go func() {
				<-start
				status, raw, err := codeModeSettingsHTTP(t.Context(), hs, http.MethodPatch, "/v1/tools", body)
				if err == nil && status != http.StatusOK {
					err = fmt.Errorf("patch status=%d body=%s", status, raw)
				}
				results <- err
			}()
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, hs, http.MethodGet, "", http.StatusOK), "only", "read")
	}
}

func TestCodeModeSettingsNewerSchemaRefusesSave(t *testing.T) {
	srv, hs := testServerWith(t, &reqRecorder{})
	path := filepath.Join(srv.cfg.Home, "toggles.json")
	if err := state.WriteJSON(path, map[string]any{"version": 99, "future": "retain"}, 0o600); err != nil {
		t.Fatal(err)
	}
	before := codeModeSettingsSaved(t, srv.cfg.Home, 99)
	codeModeSettingsRequest(t, hs, http.MethodPatch, `{"codeMode":"only","disabled":["read"]}`, http.StatusInternalServerError)
	if after := codeModeSettingsSaved(t, srv.cfg.Home, 99); !bytes.Equal(before, after) {
		t.Fatalf("newer schema overwritten: %s", after)
	}
	codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, hs, http.MethodGet, "", http.StatusOK), "mixed")
}

type codeModeSettingsGateStreamer struct {
	mu      sync.Mutex
	reqs    []loop.Request
	started chan struct{}
	release chan struct{}
	path    string
}

func (s *codeModeSettingsGateStreamer) Stream(ctx context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	first := len(s.reqs) == 1
	s.mu.Unlock()
	if first {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return types.Message{}, ctx.Err()
		}
		return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{{
			Type: "toolCall", ID: "settings-read", Name: "read", Arguments: map[string]any{"file_path": s.path},
		}}}, nil
	}
	return types.Message{Role: "assistant", StopReason: "stop", Content: []types.Content{{Type: "text", Text: "done"}}}, nil
}

func TestCodeModeSettingsOccupiedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.txt")
	if err := os.WriteFile(path, []byte("settings snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	streamer := &codeModeSettingsGateStreamer{started: make(chan struct{}), release: make(chan struct{}), path: path}
	// Release the observable gate even if a preceding assertion fails, so
	// fixture shutdown never waits for a model callback held by this test.
	defer func() {
		select {
		case <-streamer.release:
		default:
			close(streamer.release)
		}
	}()
	_, hs := testServerWith(t, streamer)
	id := createSession(t, hs, t.TempDir())
	status, raw, err := codeModeSettingsHTTP(t.Context(), hs, http.MethodPost, "/v1/sessions/"+id+"/prompt", `{"text":"inspect tool schemas","model":"openai/gpt-5.6-terra"}`)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("prompt status=%d body=%s err=%v", status, raw, err)
	}
	select {
	case <-streamer.started:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	codeModeSettingsAssertCatalog(t, codeModeSettingsRequest(t, hs, http.MethodPatch, `{"codeMode":"only"}`, http.StatusOK), "only")
	close(streamer.release)
	waitAgentEnd(t, hs, id)
	streamer.mu.Lock()
	current := slices.Clone(streamer.reqs)
	streamer.mu.Unlock()
	if len(current) != 2 {
		t.Fatalf("first occupy requests=%d", len(current))
	}
	for _, req := range current {
		codeModeSettingsAssertTools(t, req, "mixed")
	}
	codeModeSettingsPrompt(t, hs, id)
	streamer.mu.Lock()
	next := streamer.reqs[len(streamer.reqs)-1]
	streamer.mu.Unlock()
	codeModeSettingsAssertTools(t, next, "only")
}
