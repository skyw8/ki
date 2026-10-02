package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"ki/internal/config"
	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/state"
	"ki/internal/toggles"
	"ki/internal/types"
)

func writeMCPSettings(t *testing.T, path string, servers map[string]config.MCPServer) {
	t.Helper()
	err := state.WriteJSON(path, map[string]any{"version": 1, "mcpServers": servers}, 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

func mcpSettingsHTTP(t *testing.T, hs *httptest.Server, route, method, body string, status int) []byte {
	t.Helper()
	gotStatus, raw, err := codeModeSettingsHTTP(t.Context(), hs, method, route, body)
	if err != nil || gotStatus != status {
		t.Fatalf("%s %s status=%d want=%d err=%v body=%s", method, route, gotStatus, status, err, raw)
	}
	return raw
}

func decodeMCPSettings(t *testing.T, raw []byte, key string) []MCPServerInfo {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var infos []MCPServerInfo
	if err := json.Unmarshal(body[key], &infos); err != nil || infos == nil {
		t.Fatalf("%s info=%s err=%v", key, raw, err)
	}
	return infos
}

func TestMCPSettingsScopeSourcesAndNoEffects(t *testing.T) {
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "must not connect", http.StatusInternalServerError)
	}))
	defer endpoint.Close()
	srv, hs := testServerWith(t, &reqRecorder{})
	cwd := t.TempDir()
	project, _, err := srv.ws.Create(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	writeMCPSettings(t, filepath.Join(srv.cfg.Home, "mcp.json"), map[string]config.MCPServer{
		"shared": {URL: endpoint.URL, HTTPHeaders: map[string]string{"Authorization": "SECRET_CREDENTIAL"}},
		"global": {Command: "never-start", Args: []string{"SECRET_ARGUMENT"}, Env: map[string]string{"TOKEN": "SECRET_ENV"}},
	})
	writeMCPSettings(t, filepath.Join(cwd, ".ki", "mcp.json"), map[string]config.MCPServer{
		"shared": {Command: "never-start-project", Env: map[string]string{"TOKEN": "SECRET_PROJECT"}},
	})
	route := "/v1/tools?workspaceId=" + project.ID
	raw := mcpSettingsHTTP(t, hs, route, http.MethodGet, "", http.StatusOK)
	infos := decodeMCPSettings(t, raw, "mcp")
	if len(infos) != 2 || infos[0].Name != "global" || infos[0].Source != "global" || infos[0].Transport != "stdio" ||
		infos[1].Name != "shared" || infos[1].Source != "project" || infos[1].Transport != "stdio" {
		t.Fatalf("infos=%+v", infos)
	}
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "never-start") || requests.Load() != 0 || len(srv.mcpManagers) != 0 {
		t.Fatalf("catalog leaked credentials or caused effects: requests=%d body=%s", requests.Load(), raw)
	}
	id := createSession(t, hs, cwd)
	runtime := mcpSettingsHTTP(t, hs, "/v1/sessions/"+id+"?fields=runtime", http.MethodGet, "", http.StatusOK)
	if !slices.EqualFunc(infos, decodeMCPSettings(t, runtime, "availableMCP"), func(a, b MCPServerInfo) bool { return a == b }) {
		t.Fatalf("session/runtime scope mismatch: %s", runtime)
	}
	mcpSettingsHTTP(t, hs, route, http.MethodPatch, `{"mcpDisabled":["shared"],"codeMode":"only","disabled":["read"]}`, http.StatusOK)
	saved := toggles.Load(srv.cfg.Home)
	if saved.MCP.Allowed("shared") || saved.Tools.Allowed("read") || saved.CodeMode.Mode != "only" {
		t.Fatalf("saved=%+v", saved)
	}
	mcpSettingsHTTP(t, hs, route, http.MethodPatch, `{"codeMode":"off"}`, http.StatusOK)
	mcpSettingsHTTP(t, hs, route, http.MethodPatch, `{"disabled":[]}`, http.StatusOK)
	infos = decodeMCPSettings(t, mcpSettingsHTTP(t, hs, route, http.MethodGet, "", http.StatusOK), "mcp")
	if infos[1].Enabled || !infos[1].ConfiguredEnabled || toggles.Load(srv.cfg.Home).CodeMode.Mode != "off" {
		t.Fatalf("independent patches lost MCP setting: %+v", infos)
	}
	before := codeModeSettingsSaved(t, srv.cfg.Home, 2)
	for _, body := range []string{
		`{"mcpDisabled":null,"codeMode":"mixed"}`, `{"mcpDisabled":{}}`, `{"mcpDisabled":[null]}`,
		`{"mcpDisabled":[7]}`, `{"mcpDisabled":[""]}`, `{"mcpDisabled":["  "]}`, `{"mcpDisabled":["same","same"]}`,
	} {
		mcpSettingsHTTP(t, hs, route, http.MethodPatch, body, http.StatusBadRequest)
		if !bytes.Equal(before, codeModeSettingsSaved(t, srv.cfg.Home, 2)) {
			t.Fatalf("invalid patch mutated settings: %s", body)
		}
	}
}

func TestMCPSettingsDisabledRequiredNoConnection(t *testing.T) {
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "must not connect", http.StatusInternalServerError)
	}))
	defer endpoint.Close()
	for _, configuredDisabled := range []bool{false, true} {
		t.Run(fmt.Sprint(configuredDisabled), func(t *testing.T) {
			srv := codeModeTestServer(t, "off", &reqRecorder{})
			enabled := !configuredDisabled
			writeMCPSettings(t, filepath.Join(srv.cfg.Home, "mcp.json"), map[string]config.MCPServer{
				"required-server": {URL: endpoint.URL, Required: true, Enabled: &enabled},
			})
			if !configuredDisabled {
				if err := toggles.Save(srv.cfg.Home, toggles.File{MCP: session.Toggle{Disabled: []string{"required-server"}}}); err != nil {
					t.Fatal(err)
				}
			}
			id := codeModeTestSession(t, srv, t.TempDir(), "completions")
			codeModeRunPrompt(t, srv, id, "no MCP effects")
			if requests.Load() != 0 || len(srv.mcpManagers) != 0 {
				t.Fatalf("disabled required server connected: requests=%d managers=%d", requests.Load(), len(srv.mcpManagers))
			}
		})
	}
}

func TestMCPSettingsProjectExecutionAndCacheCount(t *testing.T) {
	global, project := newMCPServerFixture(t), newMCPServerFixture(t)
	cfg := mcpServerConfig(t, "off", global.url)
	streamer := codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		_, results := codeModeTurn(req)
		if len(results) == 0 {
			if _, ok := mcpRequestSpec(req, mcpSearchName); !ok {
				t.Fatal("direct fallback MCP schema absent")
			}
			return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{{
				Type: "toolCall", ID: "project-search", Name: mcpSearchName, Arguments: map[string]any{"mcp_query_unique": "project"},
			}}}, nil
		}
		if !strings.Contains(results[0].Text(), "MCP_SEARCH_RAW:project") {
			t.Fatalf("unexpected project result: %+v", results)
		}
		return codeModeStop(), nil
	})
	srv := newMCPIntegrationServer(t, cfg, streamer)
	if err := toggles.Save(cfg.Home, toggles.File{Tools: session.Toggle{Disabled: []string{"search_tool"}}}); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	writeMCPSettings(t, filepath.Join(cfg.Home, "mcp.json"), map[string]config.MCPServer{"fixture": {URL: global.url}})
	writeMCPSettings(t, filepath.Join(cwd, ".ki", "mcp.json"), map[string]config.MCPServer{"fixture": {URL: project.url}})
	id := codeModeTestSession(t, srv, cwd, "completions")
	codeModeRunPrompt(t, srv, id, "actual project override")
	if project.searchCalls.Load() != 1 || global.searchCalls.Load() != 0 {
		t.Fatalf("wrong workspace target: project=%d global=%d", project.searchCalls.Load(), global.searchCalls.Load())
	}
	snapshot, err := srv.resolveMCP(cwd)
	if err != nil {
		t.Fatal(err)
	}
	infos := srv.mcpInfos(snapshot, session.Toggle{})
	if len(infos) != 1 || infos[0].Source != "project" || infos[0].Tools == nil || *infos[0].Tools != 2 {
		t.Fatalf("cached count missing: %+v", infos)
	}
}

func TestMCPSettingsNoServeProjectFallback(t *testing.T) {
	srv := codeModeTestServer(t, "off", &reqRecorder{})
	srv.cfg.MCPServers = map[string]config.MCPServer{"serve-project": {Command: "never-start"}}
	srv.cfg.MCPFromFiles = true
	snapshot, err := srv.resolveMCP(t.TempDir())
	if err != nil || len(snapshot.Servers) != 0 {
		t.Fatalf("serve-project capability escaped into unrelated workspace: %+v err=%v", snapshot, err)
	}
	writeMCPSettings(t, filepath.Join(srv.cfg.Home, "mcp.json"), map[string]config.MCPServer{})
	srv.cfg.MCPFromFiles = false
	snapshot, err = srv.resolveMCP(t.TempDir())
	if err != nil || len(snapshot.Servers) != 0 {
		t.Fatalf("empty JSON failed to replace injected capabilities: %+v err=%v", snapshot, err)
	}
}

func TestMCPSettingsScopedPatchPreservesOtherWorkspace(t *testing.T) {
	srv, hs := testServerWith(t, &reqRecorder{})
	a, b := t.TempDir(), t.TempDir()
	disabled := false
	writeMCPSettings(t, filepath.Join(a, ".ki", "mcp.json"), map[string]config.MCPServer{
		"server-A": {Command: "never-start"},
	})
	writeMCPSettings(t, filepath.Join(b, ".ki", "mcp.json"), map[string]config.MCPServer{
		"server-B":      {Command: "never-start"},
		"file-disabled": {Command: "never-start", Enabled: &disabled},
	})
	wsA, _, err := srv.ws.Create(a, "")
	if err != nil {
		t.Fatal(err)
	}
	wsB, _, err := srv.ws.Create(b, "")
	if err != nil {
		t.Fatal(err)
	}
	routeA, routeB := "/v1/tools?workspaceId="+wsA.ID, "/v1/tools?workspaceId="+wsB.ID
	mcpSettingsHTTP(t, hs, routeA, http.MethodPatch, `{"mcpDisabled":["server-A"]}`, http.StatusOK)
	if err := srv.updateToggles(func(f *toggles.File) {
		f.MCP.Disabled = append(f.MCP.Disabled, "removed-server", "file-disabled")
	}); err != nil {
		t.Fatal(err)
	}
	mcpSettingsHTTP(t, hs, routeB, http.MethodPatch, `{"mcpDisabled":["server-B"]}`, http.StatusOK)
	mcpSettingsHTTP(t, hs, routeB, http.MethodPatch, `{"mcpDisabled":[]}`, http.StatusOK)
	got := toggles.Load(srv.cfg.Home).MCP
	if got.Allowed("server-A") || got.Allowed("removed-server") || got.Allowed("file-disabled") || !got.Allowed("server-B") {
		t.Fatalf("scope-local edit lost another workspace's settings: %+v", got)
	}
	before := codeModeSettingsSaved(t, srv.cfg.Home, 2)
	for _, body := range []string{`{"mcpDisabled":["server-A"]}`, `{"mcpDisabled":["file-disabled"]}`} {
		mcpSettingsHTTP(t, hs, routeB, http.MethodPatch, body, http.StatusBadRequest)
		if !bytes.Equal(before, codeModeSettingsSaved(t, srv.cfg.Home, 2)) {
			t.Fatal("invalid scope patch mutated state")
		}
	}
	// Reading a newer or malformed MCP document must precede every mutation,
	// even when the request only updates an unrelated tools field.
	if err := state.WriteJSON(filepath.Join(b, ".ki", "mcp.json"), map[string]any{"version": 99, "mcpServers": map[string]any{}}, 0o600); err != nil {
		t.Fatal(err)
	}
	mcpSettingsHTTP(t, hs, routeB, http.MethodPatch, `{"codeMode":"off","disabled":["read"]}`, http.StatusInternalServerError)
	if !bytes.Equal(before, codeModeSettingsSaved(t, srv.cfg.Home, 2)) {
		t.Fatal("invalid configuration allowed unrelated settings mutation")
	}
}

func TestMCPSettingsOccupiedConfigSnapshot(t *testing.T) {
	oldFixture, nextFixture := newMCPServerFixture(t), newMCPServerFixture(t)
	cfg := mcpServerConfig(t, "off", oldFixture.url)
	started, release := make(chan struct{}), make(chan struct{})
	var requestNumber atomic.Int32
	streamer := codeModeServerStreamer(func(ctx context.Context, req loop.Request) (types.Message, error) {
		_, results := codeModeTurn(req)
		if len(results) != 0 {
			return codeModeStop(), nil
		}
		if requestNumber.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return types.Message{}, ctx.Err()
			}
		}
		return types.Message{Role: "assistant", StopReason: "toolUse", Content: []types.Content{{
			Type: "toolCall", ID: fmt.Sprintf("snapshot-%d", requestNumber.Load()), Name: mcpSearchName,
			Arguments: map[string]any{"mcp_query_unique": "snapshot"},
		}}}, nil
	})
	srv := newMCPIntegrationServer(t, cfg, streamer)
	if err := toggles.Save(cfg.Home, toggles.File{Tools: session.Toggle{Disabled: []string{"search_tool"}}}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(cfg.Home, "mcp.json")
	writeMCPSettings(t, configPath, map[string]config.MCPServer{"fixture": {URL: oldFixture.url}})
	id := codeModeTestSession(t, srv, t.TempDir(), "completions")
	st, ctx, err := srv.occupy(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runPrompt(ctx, st, id, []types.Content{{Type: "text", Text: "old config"}}, nil, "", "", "", nil)
	}()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	select {
	case <-started:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	writeMCPSettings(t, configPath, map[string]config.MCPServer{"fixture": {URL: nextFixture.url}})
	// Constructing the next immutable client must not close the captured one.
	snapshot, err := srv.resolveMCP(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, releaseNext, err := srv.acquireMCP(snapshot, session.Toggle{})
	if err != nil {
		t.Fatal(err)
	}
	releaseNext()
	close(release)
	<-done
	if st.err != nil || oldFixture.searchCalls.Load() != 1 || nextFixture.searchCalls.Load() != 0 {
		t.Fatalf("captured run changed: err=%v old=%d new=%d", st.err, oldFixture.searchCalls.Load(), nextFixture.searchCalls.Load())
	}
	codeModeRunPrompt(t, srv, id, "next config")
	if nextFixture.searchCalls.Load() != 1 || oldFixture.searchCalls.Load() != 1 {
		t.Fatalf("next occupy did not update target: old=%d new=%d", oldFixture.searchCalls.Load(), nextFixture.searchCalls.Load())
	}
}

func TestMCPSettingsBoundedCachePinsActiveManagers(t *testing.T) {
	srv := &Server{}
	defer func() { _ = srv.closeMCPManagers() }()
	var releases []func()
	var firstManager any
	for i := range maxMCPManagers {
		snapshot := config.MCPSnapshot{Servers: map[string]config.MCPServer{"server": {URL: fmt.Sprintf("https://example.invalid/%d", i)}}}
		manager, release, err := srv.acquireMCP(snapshot, session.Toggle{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstManager = manager
			again, unpin, err := srv.acquireMCP(snapshot, session.Toggle{})
			if err != nil || again != firstManager {
				t.Fatal("identical configurations did not share a client")
			}
			unpin()
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	overflow := config.MCPSnapshot{Servers: map[string]config.MCPServer{"server": {URL: "https://example.invalid/overflow"}}}
	if _, _, err := srv.acquireMCP(overflow, session.Toggle{}); err == nil {
		t.Fatal("cache exceeded its bound while every client was pinned")
	}
	releases[0]()
	releases[0] = func() {}
	if _, release, err := srv.acquireMCP(overflow, session.Toggle{}); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	if len(srv.mcpManagers) != maxMCPManagers {
		t.Fatalf("cache size=%d", len(srv.mcpManagers))
	}
}
