package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"ki/internal/command"
	"ki/internal/extension"
	"ki/internal/idgen"
	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/resources"
	"ki/internal/session"
	"ki/internal/toggles"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/types"
)

var (
	errSessionBusy   = errors.New("session busy")
	errRuntimeClosed = errors.New("server shutting down")
)

func (s *Server) workspacePath(id string) string {
	if id == "" {
		return ""
	}
	rec, ok := s.ws.Get(id)
	if !ok {
		return ""
	}
	return rec.Path
}

func (s *Server) doReload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	var queued bool
	if body.SessionID == "" {
		//nolint:contextcheck // reload rewarm is process-owned via runtimeCtx
		queued = s.Reload()
	} else {
		//nolint:contextcheck // reload rewarm is process-owned via runtimeCtx
		queued = s.requestReload(body.SessionID)
	}
	slog.Info("reload")
	// Both branches change what /v1/extensions and the open session's runtime
	// catalog report; a per-session reload has no other refetch signal.
	s.publishInvalidation(scopeExtensions)
	s.publishInvalidation(scopeSessions)
	writeJSON(w, 200, map[string]any{"ok": true, "queued": queued})
}

func (s *Server) getSkills(w http.ResponseWriter, r *http.Request) {
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	tg := toggles.Load(s.cfg.Home)
	snapshot := s.resources.Scan(cwd)
	items := []map[string]any{}
	for _, item := range snapshot.Skills {
		items = append(items, map[string]any{
			"name":        item.Name,
			"description": item.Description,
			"path":        item.FilePath,
			"source":      item.Source,
			"enabled":     tg.Skills.Allowed(item.Name),
		})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// toolProfile is the single model-to-tool capability mapping used by both
// Settings and occupied runs; keeping one mapping prevents the visible catalog
// from drifting away from the request header sent to the provider.
func toolProfile(info provider.Model) builtin.Profile {
	return builtin.Profile{
		RichRead:   slices.Contains(info.Input, "image"),
		ApplyPatch: info.ApplyPatchToolType == "freeform",
	}
}

// getTools returns every globally toggleable built-in. The selected model is
// reported through available, but model-specific editors remain visible so
// changing another switch cannot erase their global disabled state.
func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	cwd, status, err := s.toolSettingsCWD(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	var profile builtin.Profile
	if sessionID != "" {
		sess, err := s.open(sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer func() { _ = sess.Close() }()
		cwd = sess.Header.CWD
		_, info, ok := s.registry.FindModel(sess.Config.Provider, sess.Config.Model)
		if !ok {
			http.Error(w, "session model unavailable", http.StatusUnprocessableEntity)
			return
		}
		profile = toolProfile(info)
		// Why this catalog does not resolve Agent depth: the tool list shown to
		// clients must match the tool list sent to the provider, which is
		// deliberately independent of depth. Admission limits active child turns
		// per root rather than changing tool exposure in a deep session.
	} else if ref := s.registry.Default(); ref.Provider != "" && ref.Model != "" {
		// Settings can be opened before a session exists. Use the last selected
		// model when it is available, while retaining a useful fallback catalog
		// if the provider catalog is not configured yet.
		if _, info, ok := s.registry.FindModel(ref.Provider, ref.Model); ok {
			profile = toolProfile(info)
		}
	}
	toolSet := builtin.Set{
		CWD: cwd, Processes: s.processesFor(sessionID), Agent: s,
		AgentParentSessionID: sessionID, Shells: s.shells, Mutations: s.mutations,
	}
	active := toolSet.Build(profile)
	available := make(map[string]bool, len(active))
	for _, tool := range active {
		available[tool.Name()] = true
	}
	builtins := toolSet.Catalog(profile)
	tg := toggles.Load(s.cfg.Home)
	mcpSnapshot, err := s.resolveMCP(cwd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	codeMode := tg.CodeMode.EffectiveMode(s.cfg.CodeMode.Mode)
	if codeMode == "mixed" || codeMode == "only" {
		available["exec"], available["wait"] = true, true
	}
	for name, server := range mcpSnapshot.Servers {
		if server.IsEnabled() && tg.MCP.Allowed(name) {
			available[catalog.SearchTool] = true
			break
		}
	}
	items := make([]map[string]any, 0, len(builtins))
	for _, tool := range builtins {
		items = append(items, map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"source":      "builtin",
			"enabled":     tg.Tools.Allowed(tool.Name()),
			"available":   available[tool.Name()],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "codeMode": codeMode, "mcp": s.mcpInfos(mcpSnapshot, tg.MCP)})
}

func (s *Server) patchTools(w http.ResponseWriter, r *http.Request) {
	var body *struct {
		Disabled    *[]string       `json:"disabled"`
		CodeMode    json.RawMessage `json:"codeMode"`
		MCPDisabled json.RawMessage `json:"mcpDisabled"`
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&body); err != nil || body == nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	// Validate the entire patch before saving: a trailing document must not
	// silently commit the first object or make malformed requests appear valid.
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	var requestedMode string
	var mcpDisabled []string
	if body.MCPDisabled != nil {
		var err error
		mcpDisabled, err = validateMCPDisabled(body.MCPDisabled)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if body.CodeMode != nil {
		if err := json.Unmarshal(body.CodeMode, &requestedMode); err != nil {
			http.Error(w, "codeMode must be off, mixed, or only", http.StatusBadRequest)
			return
		}
		switch requestedMode {
		case "off", "mixed", "only":
		default:
			http.Error(w, "codeMode must be off, mixed, or only", http.StatusBadRequest)
			return
		}
	}
	if body.Disabled != nil {
		for i, name := range *body.Disabled {
			if !catalog.IsReserved(name) {
				http.Error(w, "unknown tool "+name, http.StatusBadRequest)
				return
			}
			(*body.Disabled)[i] = toolapi.MustCanonical(name)
		}
	}
	cwd, status, err := s.toolSettingsCWD(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	mcpSnapshot, err := s.resolveMCP(cwd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	editableMCP := make(map[string]bool, len(mcpSnapshot.Servers))
	for name, server := range mcpSnapshot.Servers {
		if server.IsEnabled() {
			editableMCP[name] = true
		}
	}
	for _, name := range mcpDisabled {
		if !editableMCP[name] {
			http.Error(w, "MCP server not editable in this workspace: "+name, http.StatusBadRequest)
			return
		}
	}
	if err := s.updateToggles(func(f *toggles.File) {
		if body.Disabled != nil {
			f.Tools = session.Toggle{Disabled: *body.Disabled}
		}
		if body.CodeMode != nil {
			f.CodeMode.Mode = requestedMode
		}
		if body.MCPDisabled != nil {
			f.MCP.Disabled = mergeMCPDisabled(f.MCP.Disabled, mcpDisabled, editableMCP)
		}
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The current run keeps its request header; Reload queues the new tool
	// set for an occupied session and applies it at the next occupy boundary.
	s.Reload()
	s.getTools(w, r)
}

// updateToggles serializes read-modify-write across independent settings and
// automatic extension disablement, so saving one field cannot erase another.
func (s *Server) updateToggles(change func(*toggles.File)) error {
	s.toggleMu.Lock()
	defer s.toggleMu.Unlock()
	f := toggles.Load(s.cfg.Home)
	change(&f)
	return toggles.Save(s.cfg.Home, f)
}

func (s *Server) getCommands(w http.ResponseWriter, r *http.Request) {
	cwd := s.workspacePath(r.URL.Query().Get("workspaceId"))
	snapshot := s.resources.Scan(cwd)
	tg := toggles.Load(s.cfg.Home)
	items := command.Catalog(snapshot, tg.Skills)
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) patchSkills(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := s.updateToggles(func(f *toggles.File) {
		f.Skills = session.Toggle{Disabled: body.Disabled}
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	//nolint:contextcheck // skill toggle reload rewarm is process-owned via runtimeCtx
	s.Reload()
	s.getSkills(w, r)
}

func (s *Server) getExtensions(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.resources.Scan("")
	_ = s.disableManifestExtensions(snapshot.Extensions)
	writeJSON(w, 200, map[string]any{"items": s.extensionCatalog(snapshot, "")})
}

func (s *Server) patchExtensions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := s.updateToggles(func(f *toggles.File) {
		f.Extensions = session.Toggle{Disabled: body.Disabled}
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	//nolint:contextcheck // extension toggle reload rewarm is process-owned via runtimeCtx
	s.Reload()
	if err := s.disableManifestExtensions(s.resources.Scan("").Extensions); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.getExtensions(w, r)
	s.publishInvalidation(scopeExtensions)
	s.publishInvalidation(scopeSessions)
}

func (s *Server) extensionDescriptor(name string) (extension.Descriptor, bool) {
	discovery := extension.Discover(s.cfg.Home, toggles.Load(s.cfg.Home).Extensions)
	for _, d := range discovery.All {
		if d.Name == name {
			return d, true
		}
	}
	return extension.Descriptor{}, false
}

func (s *Server) getExtensionConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := s.extensionDescriptor(r.PathValue("name"))
	if !ok {
		http.Error(w, "extension not found", http.StatusNotFound)
		return
	}
	values, err := extension.SanitizedConfig(d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": d.Name, "schema": d.Config.Schema, "config": values, "i18n": d.I18n})
}

func (s *Server) patchExtensionConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := s.extensionDescriptor(r.PathValue("name"))
	if !ok {
		http.Error(w, "extension not found", http.StatusNotFound)
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	configRaw, ok := body["config"]
	if !ok {
		var err error
		configRaw, err = json.Marshal(body)
		if err != nil {
			http.Error(w, "invalid config", http.StatusBadRequest)
			return
		}
	}
	var patch map[string]any
	if err := json.Unmarshal(configRaw, &patch); err != nil || patch == nil {
		http.Error(w, "config must be a JSON object", http.StatusBadRequest)
		return
	}
	values, err := extension.UpdateConfig(d, patch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if s.ext != nil {
		s.ext.NotifyGlobal(d.Name, "config.updated", map[string]any{"config": values})
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": d.Name, "schema": d.Config.Schema, "config": values, "i18n": d.I18n})
}

func (s *Server) extensionCatalog(snapshot resources.Snapshot, sessionID string) []map[string]any {
	tg := toggles.Load(s.cfg.Home)
	statuses := map[string]extension.RuntimeStatus{}
	contribs := map[string]extension.SessionContribution{}
	if s.ext != nil {
		for _, state := range s.ext.RuntimeStatuses() {
			statuses[state.Name] = state
		}
		contribs = s.ext.SessionContributions(sessionID)
	}
	skillsByExt := map[string][]map[string]any{}
	for _, item := range snapshot.Skills {
		name, ok := strings.CutPrefix(item.Source, "extension:")
		if !ok {
			continue
		}
		skillsByExt[name] = append(skillsByExt[name], map[string]any{
			"name":        item.Name,
			"description": item.Description,
		})
	}
	cmdsByExt := map[string][]map[string]any{}
	seenCmd := map[string]map[string]bool{}
	for _, tmpl := range snapshot.Prompts {
		if tmpl.Extension == "" {
			continue
		}
		cmdsByExt[tmpl.Extension] = append(cmdsByExt[tmpl.Extension], map[string]any{
			"name":        tmpl.Name,
			"description": tmpl.Description,
			"source":      "prompt",
		})
		if seenCmd[tmpl.Extension] == nil {
			seenCmd[tmpl.Extension] = map[string]bool{}
		}
		seenCmd[tmpl.Extension][tmpl.Name] = true
	}
	for name, contrib := range contribs {
		for _, cmd := range contrib.Commands {
			if seenCmd[name][cmd.Name] {
				continue
			}
			cmdsByExt[name] = append(cmdsByExt[name], map[string]any{
				"name":        cmd.Name,
				"description": cmd.Description,
				"source":      "runtime",
			})
			if seenCmd[name] == nil {
				seenCmd[name] = map[string]bool{}
			}
			seenCmd[name][cmd.Name] = true
		}
	}
	for name := range skillsByExt {
		slices.SortFunc(skillsByExt[name], func(a, b map[string]any) int { return cmp.Compare(mapName(a), mapName(b)) })
	}
	for name := range cmdsByExt {
		slices.SortFunc(cmdsByExt[name], func(a, b map[string]any) int { return cmp.Compare(mapName(a), mapName(b)) })
	}
	items := []map[string]any{}
	for _, d := range snapshot.Extensions {
		errorText := d.Error
		item := map[string]any{
			"name":         d.Name,
			"version":      d.Version,
			"description":  d.Description,
			"path":         d.Path,
			"enabled":      tg.Extensions.Allowed(d.Name),
			"capabilities": d.Capabilities,
			"configurable": len(d.Config.Schema) > 0,
		}
		if d.I18n != nil {
			item["i18n"] = d.I18n
		}
		if ui := s.globalExtensionUI(d.Name); ui != nil {
			item["ui"] = ui
		}
		if state, ok := statuses[d.Name]; ok {
			item["runtime"] = state
		}
		if errorText != "" {
			item["error"] = errorText
		}
		if sk := skillsByExt[d.Name]; len(sk) > 0 {
			item["skills"] = sk
		}
		if contrib := contribs[d.Name]; len(contrib.Tools) > 0 {
			item["tools"] = contrib.Tools
		}
		if cmds := cmdsByExt[d.Name]; len(cmds) > 0 {
			item["commands"] = cmds
		}
		if files := d.PromptAppendFiles(); len(files) > 0 {
			item["promptAppend"] = files
		}
		// PATH contributions are shown with their current existence: an enabled
		// package whose directory never appears is invisible otherwise, and PATH
		// shadowing has to stay observable.
		if dirs := d.PathDirStatuses(); len(dirs) > 0 {
			item["pathDirs"] = dirs
		}
		if len(d.Providers) > 0 {
			providers := make([]map[string]any, 0, len(d.Providers))
			for _, p := range d.Providers {
				providers = append(providers, map[string]any{"id": p.ID, "name": p.Name, "api": p.API})
			}
			item["providers"] = providers
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b map[string]any) int { return cmp.Compare(mapName(a), mapName(b)) })
	return items
}

func (s *Server) onExtensionError(sessionID, name, capability, code, message string) {
	if sessionID != "" && (code == "manifest" || code == "sidecar_start" || code == "undeclared") {
		s.disableExtensions(name)
	}
	ev := loop.Event{
		Type:        loop.ExtensionError,
		Server:      name,
		Reason:      code,
		MessageText: message,
	}
	if dir, ok := s.sidx.Lookup(sessionID); ok {
		entry, err := session.AppendSidebandEvent(dir, string(loop.ExtensionError), map[string]any{
			"extension": name, "capability": capability, "code": code, "message": message,
		})
		if err != nil {
			slog.Warn("persist extension error", "session_id", sessionID, "extension", name, "err", err)
		} else {
			ev.EntryID = entry.ID
		}
	}
	s.mu.Lock()
	if st := s.runs[sessionID]; st != nil {
		st.mu.Lock()
		select {
		case <-st.done:
		default:
			st.appendLocked(&ev)
			st.wait.Broadcast()
		}
		st.mu.Unlock()
	}
	s.mu.Unlock()
	s.publishPush(sessionID, ev)
}

// occupy claims exclusive run ownership for id. The caller must pair it with
// release: runPrompt defers that; manual compaction releases after its
// prepare/execute/validate/commit pipeline.
// A second occupy while done is still open returns 409. A finished run stays
// in s.runs while the completed replay cache admits it.
func (s *Server) occupy(parent context.Context, id string) (*runState, context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeMu.Lock()
	closed := s.runtimeClosed
	deleting := s.deleting[id]
	stopping := s.stopping[id] > 0
	if !closed && !deleting && !stopping {
		s.addSessionWriterLocked(id)
	}
	s.runtimeMu.Unlock()
	if closed {
		return nil, nil, errRuntimeClosed
	}
	if deleting {
		return nil, nil, errSessionNotFound
	}
	if stopping {
		return nil, nil, errSessionBusy
	}
	// Every error before ownership is established must release this reservation.
	claimed := false
	defer func() {
		if !claimed {
			s.finishSessionWriter(id)
		}
	}()
	if st, ok := s.runs[id]; ok {
		select {
		case <-st.done:
		default:
			return nil, nil, errSessionBusy
		}
	}
	ctx, cancel := context.WithCancel(parent)
	runID, err := idgen.NewV7()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("new run id: %w", err)
	}
	st := &runState{cancel: cancel, runID: runID, done: make(chan struct{}), released: make(chan struct{}), partial: -1}
	st.wait = sync.NewCond(&st.mu)
	s.forgetReplayLocked(id)
	s.runs[id] = st
	claimed = true
	// Green dot for every other client, not just the one that prompted.
	s.publishInvalidation(scopeSessions)
	return st, ctx, nil
}

// release ends occupy: cancel the run, apply a queued resource reload, then
// close done so SSE drains and running() is false. The finished runState stays
// in s.runs so late SSE
// subscribers can replay within the completed cache's byte/TTL budget. close(done)
// before Broadcast is the events-wait protocol.
func (s *Server) release(id string, st *runState) {
	if st == nil {
		return
	}
	if st.released != nil {
		defer s.finishSessionWriter(id)
		defer close(st.released)
	}
	if st.cancel != nil {
		st.cancel()
	}
	st.mu.Lock()
	runID := st.runID
	external := cloneExternal(st.external)
	st.mu.Unlock()
	s.mu.Lock()
	pending := s.pendingReload[id]
	delete(s.pendingReload, id)
	s.mu.Unlock()
	// Idle must imply queued resource invalidation has already happened.
	// Cache accounting can be expensive; never put it between done and reload.
	if pending {
		s.reloadSession(id)
	}
	st.mu.Lock()
	st.steerClosed = true
	close(st.done)
	st.wait.Broadcast()
	st.mu.Unlock()
	// Register expiry before extension notifications or queue dispatch, which
	// can wait on unrelated work after this run has already become idle.
	s.retainReplay(id, st)
	if s.ext != nil {
		s.ext.OnEvent(context.Background(), id, extension.RedactEvent(loop.Event{Type: loop.AgentSettled, RunID: runID, External: external}, id))
	}
	s.flushSettled(id)
	// Publish after dispatchQueue: it may occupy again for a queued message, and
	// the last frame then already reflects the next run starting.
	s.dispatchQueue(id)
	s.publishInvalidation(scopeSessions)
}

func (s *Server) getMessage(w http.ResponseWriter, _ *http.Request) {
	tg := toggles.Load(s.cfg.Home)
	writeJSON(w, 200, map[string]any{"busy": tg.Message.BusyDelivery()})
}

func (s *Server) patchMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Busy string `json:"busy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Busy != toggles.BusySteer && body.Busy != toggles.BusyQueue {
		http.Error(w, "busy must be steer or queue", http.StatusBadRequest)
		return
	}
	if err := s.updateToggles(func(f *toggles.File) {
		f.Message.Busy = body.Busy
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.getMessage(w, r)
}

// steerRequest is one user-role message injected into a live run. Origin marks
// who produced it (empty is the human, agent:<task-id> a subagent or its
// completion notification, extension:<name> a relayed message); External
// carries the metadata a relaying extension needs to correlate the turn.
type steerRequest struct {
	ContextOnly     bool
	ClientRequestID string
	Completion      *types.CompletionIdentity
	Content         []types.Content
	Origin          string
	External        map[string]string
}

// pushSteerRun writes Inbox on this occupy only. Using the captured runState
// avoids steering a later occupy that replaced s.runs[id] after TakeQueueID.
//
// A false return means the message did not enter this run: the caller must
// deliver it another way (durable queue) rather than drop it.
func (s *Server) pushSteerRun(st *runState, req steerRequest) bool {
	if st == nil {
		return false
	}
	msg := types.Message{ContextOnly: req.ContextOnly, Role: "user", Content: req.Content, Origin: req.Origin, External: cloneExternal(req.External), ClientRequestID: req.ClientRequestID, Completion: req.Completion}
	if msg.ClientRequestID == "" {
		id, err := idgen.NewV7()
		if err != nil {
			return false
		}
		msg.ClientRequestID = id
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.steerClosed || st.inbox == nil {
		return false
	}
	st.inbox.Push(msg)
	// Runtime input is transcript output, not an optimistic human prompt.
	// Context-only mail can remain undrained at natural finish, and completion
	// ownership can be suppressed: neither should leave an uncommitted ghost.
	// Extension origins still relay human input, matching compact turn anchors.
	if msg.Completion != nil || msg.ContextOnly || (msg.Origin != "" && !strings.HasPrefix(msg.Origin, "extension:")) {
		return true
	}
	ev := loop.Event{Type: loop.SteerAccepted, Message: &msg, RunID: st.runID, External: cloneExternal(st.external)}
	st.appendLocked(&ev)
	st.wait.Broadcast()
	return true
}

func enableRunInbox(st *runState) {
	st.mu.Lock()
	st.inbox = &loop.Inbox{}
	st.steerClosed = false
	st.mu.Unlock()
}

func (s *Server) runAt(id string) *runState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

const (
	cancelReasonUserRequest    = "user_request"
	cancelReasonExtension      = "extension_request"
	cancelReasonSessionDelete  = "session_delete"
	cancelReasonServerShutdown = "server_shutdown"
)

// cancelRun retains the first initiating boundary before context cancellation
// erases it. Cleanup may call cancel again; it must not relabel a user stop as
// shutdown or normal release.
func (s *Server) cancelRun(sessionID string, st *runState, reason, source string, publish bool) {
	if st == nil {
		return
	}
	st.mu.Lock()
	first := st.cancelReason == ""
	if first {
		st.cancelReason = reason
		st.cancelSource = source
	}
	st.mu.Unlock()
	if publish && first {
		s.publishRunAborted(sessionID, reason, source)
	}
	if st.cancel != nil {
		st.cancel()
	}
}

func runCancellation(st *runState) (string, string) {
	if st == nil {
		return "", ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cancelReason, st.cancelSource
}

func (s *Server) publishRunAborted(sessionID, reason, source string) {
	// The Web Push fallback must stay silent for an aborted run; the mark is
	// consumed by the agent_end that follows (see notifyPushCompletion).
	s.markPushAborted(sessionID)
	ev := loop.Event{Type: loop.RunAborted, Reason: reason, CancelSource: source, Timestamp: time.Now().UnixMilli()}
	if st := s.runAt(sessionID); st != nil {
		st.mu.Lock()
		ev.RunID = st.runID
		ev.External = cloneExternal(st.external)
		st.mu.Unlock()
	}
	// Publish immediately, but persist only after the loop has written its
	// terminal assistant/tool results. Appending here as a sideband made the
	// event disappear from the branch view; appending it as a leaf here would
	// put it before the terminal partial and race the run's Session handle.
	s.publishSideband(sessionID, ev, false)
}

func (s *Server) publishQueueChanged(sessionID string) {
	s.publishSideband(sessionID, loop.Event{Type: loop.QueueChanged}, true)
}

func (s *Server) publishSideband(sessionID string, ev loop.Event, persist bool) {
	if persist {
		if dir, ok := s.sidx.Lookup(sessionID); ok {
			details := map[string]any{}
			if ev.Reason != "" {
				details["reason"] = ev.Reason
			}
			if ev.CancelSource != "" {
				details["source"] = ev.CancelSource
			}
			if ev.RunID != "" {
				details["runId"] = ev.RunID
			}
			entry, err := session.AppendSidebandEvent(dir, string(ev.Type), details)
			if err != nil {
				slog.Warn("persist sideband event", "session_id", sessionID, "type", ev.Type, "err", err)
			} else {
				ev.EntryID = entry.ID
			}
		}
	}
	s.mu.Lock()
	if st := s.runs[sessionID]; st != nil {
		st.mu.Lock()
		select {
		case <-st.done:
		default:
			st.appendLocked(&ev)
			st.wait.Broadcast()
		}
		st.mu.Unlock()
	}
	s.mu.Unlock()
	// Fan out after releasing s.mu: push subscribers never block, but keeping
	// the two locks unnested documents the order for future publishers.
	s.publishPush(sessionID, ev)
}

func (s *Server) dispatchQueue(id string) {
	gate := s.inputGate(id)
	gate.Lock()
	defer gate.Unlock()
	// Pin this dispatch on runtimeWG under the same lock as runtimeClosed so
	// Shutdown's Wait cannot return while dequeue→occupy is still in flight.
	s.runtimeMu.Lock()
	if s.runtimeClosed || s.deleting[id] || s.stopping[id] > 0 {
		s.runtimeMu.Unlock()
		return
	}
	s.addSessionWriterLocked(id)
	s.runtimeMu.Unlock()
	defer s.finishSessionWriter(id)
	if s.running(id) {
		return
	}
	dir, ok := s.sidx.Lookup(id)
	if !ok {
		return
	}
	item, ok, err := s.dequeueDispatchable(id, dir)
	if err != nil {
		slog.Warn("dequeue", "session_id", id, "err", err)
		return
	}
	if !ok {
		ext, eok, eerr := session.DequeueExtOccupy(dir)
		if eerr != nil {
			slog.Warn("dequeue ext", "session_id", id, "err", eerr)
			return
		}
		if !eok {
			if err := s.flushContextMessages(id, 0); err != nil {
				slog.Warn("flush context queue", "session_id", id, "err", err)
			}
			return
		}
		if ext.ContextBoundary {
			if ext.ContextSequence != 0 {
				if err := s.flushContextMessages(id, ext.ContextSequence); err != nil {
					if restoreErr := session.EnqueueExtFront(dir, ext); restoreErr != nil {
						slog.Warn("restore extension queue", "session_id", id, "err", restoreErr)
					}
					slog.Warn("flush context queue", "session_id", id, "err", err)
					return
				}
			}
		} else if err := s.flushContextMessages(id, 0); err != nil {
			if restoreErr := session.EnqueueExtFront(dir, ext); restoreErr != nil {
				slog.Warn("restore extension queue", "session_id", id, "err", restoreErr)
			}
			slog.Warn("flush context queue", "session_id", id, "err", err)
			return
		}
		s.publishQueueChanged(id)
		st, ctx, oerr := s.occupy(context.Background(), id)
		if oerr != nil {
			_, _ = session.EnqueueExt(dir, ext)
			s.publishQueueChanged(id)
			return
		}
		enableRunInbox(st)
		st.inputMetadata.ClientRequestID = ext.ID
		origin := ""
		if ext.Extension != "" {
			origin = "extension:" + ext.Extension
		}
		go s.runPrompt(ctx, st, id, ext.Content, nil, "", origin, ext.IdempotencyKey, nil, ext.External)
		return
	}
	s.publishQueueChanged(id)
	if err := s.flushContextMessages(id, 0); err != nil {
		if restoreErr := session.EnqueueFront(dir, item); restoreErr != nil {
			slog.Warn("requeue", "session_id", id, "err", restoreErr)
		}
		slog.Warn("flush context queue", "session_id", id, "err", err)
		return
	}
	st, ctx, err := s.occupy(context.Background(), id)
	if err != nil {
		if err := session.EnqueueFront(dir, item); err != nil {
			slog.Warn("requeue", "session_id", id, "err", err)
		} else {
			s.publishQueueChanged(id)
		}
		return
	}
	enableRunInbox(st)
	st.inputMetadata = types.Message{ClientRequestID: item.ClientRequestID, Completion: item.Completion}
	go s.runPrompt(ctx, st, id, item.Content, nil, "", item.Origin, "", s.takeNextTurn(id))
}

// dequeueDispatchable skips already-consumed generations before occupying a
// parent. This is an optimization only: explicit interruption may suppress
// a generation after dequeue, so delivery is arbitrated at persistence.
// Each skip republishes the queue rather than silently removing a visible item.
func (s *Server) dequeueDispatchable(id, dir string) (session.QueuedItem, bool, error) {
	for {
		item, ok, err := session.Dequeue(dir)
		if err != nil || !ok {
			return session.QueuedItem{}, ok, err
		}
		if item.Completion != nil && s.agentTasks != nil && s.agentTasks.CompletionDelivered(*item.Completion) {
			s.publishQueueChanged(id)
			continue
		}
		return item, true, nil
	}
}

func writeHandled(w http.ResponseWriter, notice string, isErr bool) {
	writeJSON(w, 200, map[string]any{"handled": true, "notice": notice, "error": isErr})
}

func contentText(content []types.Content) string {
	var b strings.Builder
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func hasNonText(content []types.Content) bool {
	return slices.ContainsFunc(content, func(c types.Content) bool { return c.Type != "text" })
}

// mapName returns a catalog row's "name" field, treating a non-string as empty
// so sorting stays total.
func mapName(row map[string]any) string {
	name, _ := row["name"].(string)
	return name
}

func (s *Server) startRun(parent context.Context, w http.ResponseWriter, id string, content []types.Content, parentID *string, model string, clientRequestID ...string) {
	gate := s.inputGate(id)
	gate.Lock()
	defer gate.Unlock()
	if err := s.sessionAdmissionError(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := s.flushContextMessages(id, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	st, ctx, err := s.occupy(parent, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	enableRunInbox(st)
	st.inputMetadata.ClientRequestID = st.runID + ":input"
	if len(clientRequestID) > 0 && clientRequestID[0] != "" {
		st.inputMetadata.ClientRequestID = clientRequestID[0]
	}
	// runPrompt's defer release returns this occupy slot.
	go s.runPrompt(ctx, st, id, content, parentID, model, "", "", s.takeNextTurn(id))
	writeJSON(w, 202, map[string]any{"session_id": id, "accepted": "started", "clientRequestId": st.inputMetadata.ClientRequestID})
}
