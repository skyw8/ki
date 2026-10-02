package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ki/internal/config"
	"ki/internal/server"
	"ki/internal/state"
)

type thinkingRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

// Handled prompts exercise only HTTP orchestration: no model, SSE timing, or
// in-process server is needed to verify durable settings precede submission.
func thinkingClientFixture(t *testing.T, patchStatus int) (config.Config, func() []thinkingRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []thinkingRequest
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/abort") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var body map[string]any
		if r.Method != http.MethodGet {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode %s %s: %v", r.Method, r.URL.Path, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("missing server-file bearer token")
			}
		}
		mu.Lock()
		requests = append(requests, thinkingRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			_, _ = w.Write([]byte(`{"id":"created"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/sessions/resumed":
			if patchStatus >= http.StatusBadRequest {
				http.Error(w, "settings rejected", patchStatus)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"):
			_, _ = w.Write([]byte(`{"handled":true}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(hs.Close)
	cfg := config.Config{Home: t.TempDir()}
	file := struct {
		Version int `json:"version"`
		server.File
	}{Version: 1, File: server.File{Addr: strings.TrimPrefix(hs.URL, "http://"), Token: "test-token"}}
	if err := state.WriteJSON(filepath.Join(cfg.Home, "server.json"), file, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg, func() []thinkingRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]thinkingRequest(nil), requests...)
	}
}

func TestRunThinkingCreate(t *testing.T) {
	for _, thinking := range []string{"", "off fast", "high fast"} {
		t.Run(thinking, func(t *testing.T) {
			cfg, requests := thinkingClientFixture(t, http.StatusOK)
			f := flags{CWD: t.TempDir(), Model: "openai-codex/model", Thinking: thinking}
			var err error
			captureStdout(t, func() { err = runClient(cfg, f, "hello") })
			if err != nil {
				t.Fatal(err)
			}
			create := map[string]any{"cwd": f.CWD, "model": f.Model}
			if thinking != "" {
				create["thinkingEffort"] = thinking
			}
			want := []thinkingRequest{
				{Method: http.MethodGet, Path: "/v1/health"},
				{Method: http.MethodPost, Path: "/v1/sessions", Body: create},
				{Method: http.MethodPost, Path: "/v1/sessions/created/prompt", Body: map[string]any{"text": "hello", "model": f.Model}},
			}
			if got := requests(); !reflect.DeepEqual(got, want) {
				t.Fatalf("requests = %#v; want %#v", got, want)
			}
		})
	}
}

func TestRunThinkingResumeAtomicPatch(t *testing.T) {
	for _, model := range []string{"", "openai-codex/new-model"} {
		t.Run(model, func(t *testing.T) {
			cfg, requests := thinkingClientFixture(t, http.StatusOK)
			f := flags{Session: "resumed", Model: model, Thinking: "medium fast"}
			var err error
			captureStdout(t, func() { err = runClient(cfg, f, "hello") })
			if err != nil {
				t.Fatal(err)
			}
			patch := map[string]any{"thinkingEffort": "medium fast"}
			if model != "" {
				patch["model"] = model
			}
			want := []thinkingRequest{
				{Method: http.MethodGet, Path: "/v1/health"},
				{Method: http.MethodPatch, Path: "/v1/sessions/resumed", Body: patch},
				{Method: http.MethodPost, Path: "/v1/sessions/resumed/prompt", Body: map[string]any{"text": "hello", "model": model}},
			}
			if got := requests(); !reflect.DeepEqual(got, want) {
				t.Fatalf("requests = %#v; want %#v", got, want)
			}
		})
	}
}

func TestRunResumeWithoutThinkingDoesNotPatch(t *testing.T) {
	cfg, requests := thinkingClientFixture(t, http.StatusOK)
	f := flags{Session: "resumed", Model: "new-model", Queue: true}
	var err error
	captureStdout(t, func() { err = runClient(cfg, f, "hello") })
	if err != nil {
		t.Fatal(err)
	}
	want := []thinkingRequest{
		{Method: http.MethodGet, Path: "/v1/health"},
		{Method: http.MethodPost, Path: "/v1/sessions/resumed/prompt", Body: map[string]any{"text": "hello", "model": f.Model, "delivery": "queue"}},
	}
	if got := requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v; want %#v", got, want)
	}
}

func TestRunThinkingPatchFailureNeverSubmitsPrompt(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			cfg, requests := thinkingClientFixture(t, status)
			err := runClient(cfg, flags{Session: "resumed", Model: "new-model", Thinking: "high fast"}, "hello")
			if !errors.Is(err, errHTTPResponse) || !strings.Contains(err.Error(), "settings rejected") {
				t.Fatalf("error = %v", err)
			}
			got := requests()
			if len(got) != 2 || got[1].Method != http.MethodPatch {
				t.Fatalf("rejected PATCH submitted additional requests: %#v", got)
			}
		})
	}
}

func TestRunThinkingFlagAcceptsAllCanonicalChoices(t *testing.T) {
	for _, base := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		for _, choice := range []string{base, base + " fast"} {
			t.Run(choice, func(t *testing.T) {
				cmd := newRunCommand()
				if err := cmd.ParseFlags([]string{"--thinking", choice, "hello"}); err != nil {
					t.Fatal(err)
				}
				got, err := cmd.Flags().GetString("thinking")
				if err != nil || got != choice || !reflect.DeepEqual(cmd.Flags().Args(), []string{"hello"}) {
					t.Fatalf("flag = %q, args = %v, error = %v", got, cmd.Flags().Args(), err)
				}
			})
		}
	}
}

func TestTranscriptThinkingFlagRemainsDisplayOnly(t *testing.T) {
	cmd, _, err := newSessionCommand().Find([]string{"show"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags([]string{"--thinking", "session-id"}); err != nil {
		t.Fatal(err)
	}
	if enabled, err := cmd.Flags().GetBool("thinking"); err != nil || !enabled {
		t.Fatalf("transcript thinking flag = %v, error = %v", enabled, err)
	}
}

func TestRunInvalidInputHasNoServerEffects(t *testing.T) {
	cfg, requests := thinkingClientFixture(t, http.StatusOK)
	for _, tc := range []struct {
		name   string
		prompt string
		flags  flags
		want   error
	}{
		{name: "empty", prompt: " \t ", flags: flags{Thinking: "high fast"}, want: errPromptRequired},
		{name: "conflicting delivery", prompt: "hello", flags: flags{Session: "resumed", Thinking: "off fast", Steer: true, Queue: true}, want: errSteerQueueExclusive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runClient(cfg, tc.flags, tc.prompt); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
			if got := requests(); len(got) != 0 {
				t.Fatalf("invalid input reached server: %#v", got)
			}
		})
	}
}

func TestRunInvalidInputPrecedesConfigAndLogging(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(home, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KI_HOME", home)
	for _, tc := range []struct {
		args []string
		want error
	}{
		{args: []string{"--thinking", "high fast", " "}, want: errPromptRequired},
		{args: []string{"--thinking", "off fast", "--steer", "--queue", "hello"}, want: errSteerQueueExclusive},
	} {
		cmd := newRunCommand()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(tc.args)
		if err := cmd.Execute(); !errors.Is(err, tc.want) {
			t.Fatalf("args %v: error = %v; want %v", tc.args, err, tc.want)
		}
	}
}
