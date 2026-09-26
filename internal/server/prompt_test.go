package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ki/internal/prompt"
	"ki/internal/resources"
	"ki/internal/workspace"
)

type promptAppendItem struct {
	Source    string `json:"source"`
	Editable  bool   `json:"editable"`
	Available bool   `json:"available"`
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	Text      string `json:"text"`
	Bytes     int    `json:"bytes"`
	Reason    string `json:"reason"`
	Name      string `json:"name"`
}

type promptAppendView struct {
	Items     []promptAppendItem `json:"items"`
	Effective string             `json:"effective"`
}

func promptAppendRequest(t *testing.T, hs *httptest.Server, method, query string, body any) promptAppendView {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := marshalJSON(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(t.Context(), method, hs.URL+"/v1/prompt/append"+query, reader)
	req.Header.Set("Authorization", "Bearer tok")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, query, res.StatusCode, raw)
	}
	var got promptAppendView
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s %s: %v (%s)", method, query, err, raw)
	}
	return got
}

func promptAppendSource(t *testing.T, view promptAppendView, source string) promptAppendItem {
	t.Helper()
	for _, item := range view.Items {
		if item.Source == source {
			return item
		}
	}
	t.Fatalf("source %q missing from %+v", source, view.Items)
	return promptAppendItem{}
}

func createTestWorkspace(t *testing.T, hs *httptest.Server, path string) string {
	t.Helper()
	body, _ := marshalJSON(map[string]any{"path": path})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL+"/v1/workspaces", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace %d %s", res.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("no workspace id: %s", raw)
	}
	return id
}

func TestPromptAppendListsSources(t *testing.T) {
	srv, hs := testServer(t)
	got := promptAppendRequest(t, hs, http.MethodGet, "", nil)

	builtin := promptAppendSource(t, got, "builtin")
	if builtin.Editable || !builtin.Available || builtin.Text != prompt.DefaultAppendSystemPrompt {
		t.Fatalf("builtin item = %+v", builtin)
	}
	if !strings.Contains(got.Effective, prompt.DefaultAppendSystemPrompt) {
		t.Fatalf("effective append stack lacks the built-in layer: %q", got.Effective)
	}
	global := promptAppendSource(t, got, "global")
	if !global.Editable || !global.Available || global.Exists {
		t.Fatalf("global item = %+v", global)
	}
	wantGlobal := filepath.Join(srv.cfg.Home, "prompt", "APPEND_SYSTEM.md")
	if global.Path != wantGlobal {
		t.Fatalf("global path = %q, want %q", global.Path, wantGlobal)
	}
	project := promptAppendSource(t, got, "project")
	if !project.Editable || project.Available || project.Reason != "workspace-required" || project.Path != "" {
		t.Fatalf("project item without workspace = %+v", project)
	}
}

// TestPromptAppendWritesBothSourcesAdditively pins the write path, the file
// permissions, and the additive read-back: the project file must not hide the
// global one, and a saved file must reach a session's snapshot.
func TestPromptAppendWritesBothSourcesAdditively(t *testing.T) {
	srv, hs := testServer(t)
	cwd := t.TempDir()
	wsID := createTestWorkspace(t, hs, cwd)
	query := "?workspaceId=" + url.QueryEscape(wsID)

	view := promptAppendRequest(t, hs, http.MethodGet, query, nil)
	if !promptAppendSource(t, view, "project").Available {
		t.Fatalf("project source unavailable with a workspace: %+v", view.Items)
	}

	view = promptAppendRequest(t, hs, http.MethodPut, "", map[string]any{"source": "global", "text": "GLOBAL-RULE"})
	if item := promptAppendSource(t, view, "global"); !item.Exists || item.Text != "GLOBAL-RULE" || item.Bytes != len("GLOBAL-RULE") {
		t.Fatalf("global after write = %+v", item)
	}
	globalPath := filepath.Join(srv.cfg.Home, "prompt", "APPEND_SYSTEM.md")
	st, err := os.Stat(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("global prompt mode = %v, want 0600", st.Mode().Perm())
	}

	view = promptAppendRequest(t, hs, http.MethodPut, query, map[string]any{"source": "project", "text": "PROJECT-RULE"})
	project := promptAppendSource(t, view, "project")
	if !project.Exists || project.Text != "PROJECT-RULE" {
		t.Fatalf("project after write = %+v", project)
	}
	// The workspace registry stores the normalized path, so the expectation has
	// to be normalized too: t.TempDir is unresolved on macOS (/var ->
	// /private/var) and under Windows short-name %TEMP% paths.
	normalized, err := workspace.Normalize(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(normalized, ".ki", "prompt", "APPEND_SYSTEM.md"); project.Path != want {
		t.Fatalf("project path = %q, want %q", project.Path, want)
	}
	builtinAt := strings.Index(view.Effective, prompt.DefaultAppendSystemPrompt)
	globalAt := strings.Index(view.Effective, "GLOBAL-RULE")
	projectAt := strings.Index(view.Effective, "PROJECT-RULE")
	if builtinAt < 0 || globalAt < 0 || projectAt < 0 || !(builtinAt < globalAt && globalAt < projectAt) {
		t.Fatalf("effective order builtin=%d global=%d project=%d: %q", builtinAt, globalAt, projectAt, view.Effective)
	}

	// The settings write must reach sessions, not only the settings view: a
	// cached snapshot would keep serving the prompt from before the edit.
	snapshot := srv.resources.Load("prompt-append-session", cwd)
	if len(snapshot.AppendSystemPrompts) != 2 ||
		snapshot.AppendSystemPrompts[0].Text != "GLOBAL-RULE" ||
		snapshot.AppendSystemPrompts[1].Text != "PROJECT-RULE" {
		t.Fatalf("snapshot append layers = %+v", snapshot.AppendSystemPrompts)
	}

	view = promptAppendRequest(t, hs, http.MethodDelete, query+"&source=project", nil)
	project = promptAppendSource(t, view, "project")
	if project.Exists || project.Text != "" {
		t.Fatalf("project after delete = %+v", project)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".ki", "prompt", "APPEND_SYSTEM.md")); !os.IsNotExist(err) {
		t.Fatalf("project file still present: %v", err)
	}
	if strings.Contains(view.Effective, "PROJECT-RULE") {
		t.Fatalf("effective still holds the deleted layer: %q", view.Effective)
	}
	// Deleting a missing file is not an error: the wanted state is "no file".
	promptAppendRequest(t, hs, http.MethodDelete, query+"&source=project", nil)
}

func TestPromptAppendRejectsInvalidWrites(t *testing.T) {
	_, hs := testServer(t)
	cases := []struct {
		name  string
		query string
		body  map[string]any
		want  int
	}{
		{"unknown source", "", map[string]any{"source": "extension", "text": "x"}, http.StatusBadRequest},
		{"missing source", "", map[string]any{"text": "x"}, http.StatusBadRequest},
		{"empty text", "", map[string]any{"source": "global", "text": ""}, http.StatusBadRequest},
		{"project without workspace", "", map[string]any{"source": "project", "text": "x"}, http.StatusBadRequest},
		{"oversized text", "", map[string]any{"source": "global", "text": strings.Repeat("x", maxAppendSystemPromptBytes+1)}, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := marshalJSON(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, hs.URL+"/v1/prompt/append"+tc.query, bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer tok")
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

// TestPromptAppendRejectsUnknownWorkspace pins the write boundary: an
// unregistered workspace id must not create a .ki tree at an arbitrary path.
func TestPromptAppendRejectsUnknownWorkspace(t *testing.T) {
	srv, hs := testServer(t)
	query := "?workspaceId=nope"
	raw, _ := marshalJSON(map[string]any{"source": "project", "text": "x"})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, hs.URL+"/v1/prompt/append"+query, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(srv.cfg.Home, "prompt", "APPEND_SYSTEM.md")); !os.IsNotExist(err) {
		t.Fatalf("unexpected global write: %v", err)
	}
}

func TestAppendSystemPromptPathSources(t *testing.T) {
	if _, ok := resources.AppendSystemPromptPath("", "", resources.AppendSourceGlobal); ok {
		t.Fatal("global source without home must not resolve")
	}
	if _, ok := resources.AppendSystemPromptPath("/home", "", resources.AppendSourceProject); ok {
		t.Fatal("project source without cwd must not resolve")
	}
	if _, ok := resources.AppendSystemPromptPath("/home", "/cwd", "nope"); ok {
		t.Fatal("unknown source must not resolve")
	}
}
