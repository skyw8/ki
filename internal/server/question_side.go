package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ki/internal/resources"
	"ki/internal/session"
	toolapi "ki/internal/tool"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/types"
)

const questionSidePurpose = "question_side"

const questionSidePolicy = `You are in a separate, read-only side conversation helping the user understand and answer a question from the main conversation.
Inherited history is background only. Do not continue its tasks, plans, tool calls, approvals, or agent work. Respond only to instructions after the side-conversation boundary.
You may explain alternatives and read the permitted workspace or attachments. You cannot execute commands, change files, invoke external actions, create agents, or ask another request_user_input_async question.
Do not treat your suggestions as the user's authorization. Nothing in this conversation is automatically sent to the main model; the user must explicitly submit their final answer.`

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func isQuestionSide(cfg session.Config) bool {
	return metadataString(cfg.Metadata, "purpose") == questionSidePurpose
}

func addQuestionSideMetadata(body map[string]any, metadata map[string]any) {
	for _, key := range []string{"purpose", "sourceSessionId", "sourceQuestionItemId", "sideBoundaryEntryId"} {
		if value := metadataString(metadata, key); value != "" {
			body[key] = value
		}
	}
}

func (s *Server) forkQuestionSide(w http.ResponseWriter, r *http.Request, source *session.Session, questionID string) {
	gate := s.questionGate(source.ID())
	gate.Lock()
	defer gate.Unlock()
	if isQuestionSide(source.Config) {
		http.Error(w, "nested side conversations are not allowed", http.StatusConflict)
		return
	}
	q, ok := source.Question(questionID)
	if !ok {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	if !q.SourceBranchValid {
		http.Error(w, "question is not on the active branch", http.StatusConflict)
		return
	}
	if q.SideSessionID != "" {
		side, err := s.open(q.SideSessionID)
		if err == nil {
			defer func() { _ = side.Close() }()
			if !isQuestionSide(side.Config) ||
				metadataString(side.Config.Metadata, "sourceSessionId") != source.ID() ||
				metadataString(side.Config.Metadata, "sourceQuestionItemId") != questionID {
				http.Error(w, "question side association is invalid", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, s.sessionMap(side, map[string]any{"question": q.Public()}))
			return
		}
	}
	recovered, err := s.recoverQuestionSide(source.ID(), questionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if recovered != nil {
		defer func() { _ = recovered.Close() }()
		q.SideSessionID = recovered.ID()
		q.Revision++
		if err := source.UpdateQuestion(q); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		s.publishQuestion(source.ID(), q)
		s.sidx.Add(recovered.ID(), recovered.Dir)
		writeJSON(w, http.StatusOK, s.sessionMap(recovered, map[string]any{"question": q.Public()}))
		s.publishInvalidation(scopeSessions)
		return
	}
	side, err := s.createQuestionSide(source, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = side.Close() }()
	q.SideSessionID = side.ID()
	q.Revision++
	if err := source.UpdateQuestion(q); err != nil {
		_ = side.Close()
		_ = os.RemoveAll(side.Dir)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.publishQuestion(source.ID(), q)
	if rec, ok := s.ws.Match(side.Header.CWD); ok {
		_ = s.ws.AttachSession(rec.ID, side.ID())
	}
	s.sidx.Add(side.ID(), side.Dir)
	writeJSON(w, http.StatusOK, s.sessionMap(side, map[string]any{"question": q.Public()}))
	s.publishInvalidation(scopeSessions)
	s.publishInvalidation(scopeWorkspaces)
}

func (s *Server) recoverQuestionSide(sourceID, questionID string) (*session.Session, error) {
	infos, err := s.slist.List(s.cfg.Sessions.Root)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(infos, func(a, b session.Info) int { return strings.Compare(a.Timestamp, b.Timestamp) })
	for _, info := range infos {
		if metadataString(info.Metadata, "purpose") != questionSidePurpose ||
			metadataString(info.Metadata, "sourceSessionId") != sourceID ||
			metadataString(info.Metadata, "sourceQuestionItemId") != questionID {
			continue
		}
		side, err := session.Open(info.Dir)
		if err != nil {
			return nil, err
		}
		if metadataString(side.Config.Metadata, "sideInitialization") == "ready" &&
			entryIn(side.Entries(), metadataString(side.Config.Metadata, "sideBoundaryEntryId")) {
			return side, nil
		}
		_ = side.Close()
		if s.running(info.ID) {
			return nil, errors.New("side conversation initialization is still running")
		}
		if err := os.RemoveAll(info.Dir); err != nil {
			return nil, err
		}
		s.sidx.Remove(info.ID)
	}
	return nil, nil
}

func (s *Server) createQuestionSide(source *session.Session, q session.Question) (*session.Session, error) {
	cwd := q.SnapshotCWD
	if cwd == "" {
		return nil, errors.New("question has no frozen workspace")
	}
	header := source.Header
	header.CWD = cwd
	frozenSource := session.OpenFrom(source.Dir, header, source.Config, source.Entries())
	metadata := map[string]any{
		"purpose":               questionSidePurpose,
		"sourceSessionId":       source.ID(),
		"sourceQuestionItemId":  q.QuestionItemID,
		"snapshotEntryId":       q.SnapshotEntryID,
		"readOnlyProfile":       "workspace_read",
		"readOnlyWorkspaceRoot": cwd,
		"sideInitialization":    "pending",
	}
	if len(q.SnapshotResources) > 0 {
		metadata["questionSideResources"] = q.SnapshotResources
	}
	var side *session.Session
	var err error
	if q.SnapshotEntryID == "" {
		side, err = session.CreateChildWithMetadata(s.cfg.Sessions.Root, frozenSource, metadata)
	} else {
		entries := session.LeafChain(source.Entries(), q.SnapshotEntryID)
		var messages []types.Message
		for _, entry := range entries {
			if entry.Message != nil {
				messages = append(messages, *entry.Message)
			}
		}
		if hasUnresolvedToolCalls(messages) {
			return nil, errors.New("question snapshot contains unresolved tool calls")
		}
		side, err = session.ForkHistoryAtWithMetadata(s.cfg.Sessions.Root, frozenSource, q.SnapshotEntryID, metadata, session.ForkModeTree)
	}
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*session.Session, error) {
		_ = side.Close()
		_ = os.RemoveAll(side.Dir)
		return nil, err
	}
	attachments := filepath.Join(side.Dir, "attachments")
	if err := os.MkdirAll(attachments, 0700); err != nil {
		return fail(err)
	}
	attachmentRoot, err := filepath.EvalSymlinks(attachments)
	if err != nil {
		return fail(err)
	}
	side.Config.Metadata["readOnlyAttachmentsRoot"] = attachmentRoot
	side.Config.Provider = source.Config.Provider
	side.Config.Model = source.Config.Model
	side.Config.ThinkingEffort = source.Config.ThinkingEffort
	side.Config.CacheKey = source.CacheAffinityKey()
	side.Config.Title = q.Title
	side.Config.Pinned, side.Config.PinnedAt = false, ""
	if err := side.SaveConfig(); err != nil {
		return fail(err)
	}
	if q.SupplementalUser != nil {
		message, err := session.CopyMessageAttachments(source.Dir, side.Dir, *q.SupplementalUser)
		if err != nil {
			return fail(err)
		}
		if _, err := side.AppendMessage(message); err != nil {
			return fail(err)
		}
	}
	question, err := json.Marshal(struct {
		Title   string   `json:"title"`
		Options []string `json:"options,omitempty"`
	}{q.Title, q.Options})
	if err != nil {
		return fail(err)
	}
	boundary, err := side.AppendMessage(types.Message{
		Role: "user", Origin: "runtime:question-side",
		Content: []types.Content{{Type: "text", Text: "Side-conversation boundary. Earlier history is background only. Help the user understand the following main-conversation question; do not submit an answer on their behalf.\n" + string(question)}},
	})
	if err != nil {
		return fail(err)
	}
	side.Config.Metadata["sideBoundaryEntryId"] = boundary.ID
	side.Config.Metadata["sideInitialization"] = "ready"
	if err := side.SaveConfig(); err != nil {
		return fail(err)
	}
	return side, nil
}

type questionSideTool struct {
	toolapi.Tool
	cwd   string
	roots []string
}

func (t questionSideTool) Prompt() string {
	if toolapi.Equal(t.Name(), catalog.Read) {
		return "Read a file within this side conversation's permitted workspace or attachments. Paths outside these roots are rejected. " + t.Description()
	}
	return "Search only the permitted workspace and attachments using bounded, root-confined search. Grep supports Go RE2 regular expressions; unsupported syntax is rejected. Ignore rules come only from the searched workspace, not host configuration. File, byte, result and time budgets may truncate results."
}

func (t questionSideTool) Description() string {
	if toolapi.Equal(t.Name(), catalog.Grep) {
		return "Search permitted files using bounded, root-confined RE2 matching."
	}
	if toolapi.Equal(t.Name(), catalog.Glob) {
		return "Find permitted files using bounded, root-confined glob matching."
	}
	return t.Tool.Description()
}

func (t questionSideTool) Validate(args map[string]any) error {
	if validator, ok := t.Tool.(toolapi.Validator); ok {
		return validator.Validate(args)
	}
	return nil
}

func (t questionSideTool) Execute(ctx context.Context, args map[string]any) toolapi.Result {
	key := "path"
	if toolapi.Equal(t.Name(), catalog.Read) {
		key = "file_path"
	}
	path, _ := args[key].(string)
	if path == "" {
		path = t.cwd
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.cwd, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		err = questionSidePathAllowed(resolved, t.roots)
	}
	if err != nil {
		return questionSideError(fmt.Sprintf("side conversation path rejected: %v", err))
	}
	if toolapi.Equal(t.Name(), catalog.Glob) {
		pattern, _ := args["pattern"].(string)
		for _, part := range strings.FieldsFunc(pattern, func(r rune) bool { return r == '/' || r == '\\' }) {
			if part == ".." {
				return questionSideError("side conversation glob cannot traverse parent directories")
			}
		}
		if filepath.IsAbs(pattern) || filepath.VolumeName(pattern) != "" {
			return questionSideError("use a relative glob pattern and an allowed path")
		}
	}
	args = maps.Clone(args)
	args[key] = resolved
	if !toolapi.Equal(t.Name(), catalog.Read) {
		return executeQuestionSideSearch(ctx, toolapi.MustCanonical(t.Name()), args, t.cwd, t.roots)
	}
	return t.Tool.Execute(ctx, args)
}

func questionSideError(message string) toolapi.Result {
	return toolapi.Result{IsError: true, Content: []types.Content{{Type: "text", Text: message}}}
}

func questionSidePathAllowed(path string, roots []string) error {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && filepath.IsLocal(rel) {
			return nil
		}
	}
	return errors.New("outside permitted workspace and attachments")
}

func questionSideRoots(sess *session.Session) []string {
	var roots []string
	workspace := metadataString(sess.Config.Metadata, "readOnlyWorkspaceRoot")
	if workspace == "" {
		return nil
	}
	for _, path := range []string{workspace, metadataString(sess.Config.Metadata, "readOnlyAttachmentsRoot")} {
		if filepath.IsAbs(path) {
			roots = append(roots, filepath.Clean(path))
		}
	}
	return roots
}

func questionSideResources(sess *session.Session) (resources.Snapshot, error) {
	value, ok := sess.Config.Metadata["questionSideResources"]
	if !ok {
		return resources.Snapshot{}, errors.New("question side has no frozen prompt resources")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return resources.Snapshot{}, err
	}
	var snapshot resources.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return resources.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Server) syncQuestionSideModel(side *session.Session) error {
	if !isQuestionSide(side.Config) {
		return nil
	}
	dir, err := s.sessionDir(metadataString(side.Config.Metadata, "sourceSessionId"))
	if err != nil {
		return err
	}
	parent, err := session.ReadConfig(dir)
	if err != nil {
		return err
	}
	if side.Config.Provider == parent.Provider && side.Config.Model == parent.Model && side.Config.ThinkingEffort == parent.ThinkingEffort {
		return nil
	}
	return side.SetModelAndThinking(parent.Provider, parent.Model, parent.ThinkingEffort)
}

func (s *Server) syncQuestionSideModels(parent *session.Session) error {
	if isQuestionSide(parent.Config) {
		return nil
	}
	infos, err := s.slist.List(s.cfg.Sessions.Root)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if metadataString(info.Metadata, "purpose") != questionSidePurpose || metadataString(info.Metadata, "sourceSessionId") != parent.ID() {
			continue
		}
		gate := s.inputGate(info.ID)
		gate.Lock()
		err := s.mutateIdleSession(info.ID, func() error {
			side, err := session.Open(info.Dir)
			if err != nil {
				return err
			}
			defer func() { _ = side.Close() }()
			return s.syncQuestionSideModel(side)
		})
		gate.Unlock()
		if err != nil && !errors.Is(err, errSessionBusy) {
			return err
		}
	}
	return nil
}

func (s *Server) isQuestionSideSession(id string) bool {
	dir, err := s.sessionDir(id)
	if err != nil {
		return false
	}
	cfg, err := session.ReadConfig(dir)
	return err == nil && isQuestionSide(cfg)
}

func publicQuestionSideMetadata(metadata map[string]any) map[string]any {
	return session.PublicMetadata(metadata)
}

func restrictQuestionSideTools(sess *session.Session, tools []toolapi.Tool) []toolapi.Tool {
	if !isQuestionSide(sess.Config) {
		return tools
	}
	roots := questionSideRoots(sess)
	out := make([]toolapi.Tool, 0, 3)
	for _, tool := range tools {
		switch toolapi.MustCanonical(tool.Name()) {
		case catalog.Read, catalog.Grep, catalog.Glob:
			out = append(out, questionSideTool{Tool: tool, cwd: sess.Header.CWD, roots: roots})
		}
	}
	return out
}

type questionSideReadOps struct {
	roots  []string
	pinned map[string]*os.Root
}

func (ops questionSideReadOps) open(path string) (*os.Root, string, error) {
	for _, allowed := range ops.roots {
		rel, err := filepath.Rel(allowed, path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		if root := ops.pinned[allowed]; root != nil {
			duplicate, err := root.OpenRoot(".")
			return duplicate, rel, err
		}
		root, err := openPinnedSideRoot(allowed)
		return root, rel, err
	}
	return nil, "", errors.New("outside permitted side conversation roots")
}

func pinQuestionSideReadOps(roots []string) (*questionSideReadOps, error) {
	ops := &questionSideReadOps{roots: roots, pinned: map[string]*os.Root{}}
	for _, path := range roots {
		root, err := openPinnedSideRoot(path)
		if err != nil {
			ops.Close()
			return nil, err
		}
		ops.pinned[path] = root
	}
	return ops, nil
}

func (ops *questionSideReadOps) Close() {
	for _, root := range ops.pinned {
		_ = root.Close()
	}
}

func (ops questionSideReadOps) Stat(ctx context.Context, path string) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, rel, err := ops.open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Stat(rel)
}

func (ops questionSideReadOps) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, rel, err := ops.open(path)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(rel)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &sideReadFile{File: file, root: root}, nil
}

type sideReadFile struct {
	*os.File
	root *os.Root
}

func (f *sideReadFile) Close() error {
	err := f.File.Close()
	return errors.Join(err, f.root.Close())
}

func (ops questionSideReadOps) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, rel, err := ops.open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(rel)
}

func questionSideVisibleEntries(entries []session.Entry, boundaryID string) []session.Entry {
	at := slices.IndexFunc(entries, func(entry session.Entry) bool { return entry.ID == boundaryID })
	if at < 0 {
		return []session.Entry{}
	}
	visible := slices.Clone(entries[at+1:])
	if len(visible) > 0 {
		visible[0].ParentID = ""
	}
	return visible
}

func questionSideTargetAllowed(sess *session.Session, target string) bool {
	if !isQuestionSide(sess.Config) {
		return true
	}
	if target == "" {
		return false
	}
	boundary := metadataString(sess.Config.Metadata, "sideBoundaryEntryId")
	return boundary != "" && slices.ContainsFunc(session.LeafChain(sess.Entries(), target), func(entry session.Entry) bool {
		return entry.ID == boundary
	})
}

func questionSideAllowsEntry(sess *session.Session, target string) bool {
	return questionSideTargetAllowed(sess, target)
}

func (s *Server) getQuestionSide(w http.ResponseWriter, r *http.Request, snap *sessionSnap, limit, keep int) {
	entries, err := session.AllEntries(snap.dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	visible := questionSideVisibleEntries(entries, metadataString(snap.configV.Metadata, "sideBoundaryEntryId"))
	leaf := snap.leafID
	if !entryIn(visible, leaf) {
		leaf = lastNonSideband(visible)
	}
	query := r.URL.Query()
	entryID, batch := query.Get("entry"), query.Get("entries")
	if entryID != "" || batch != "" {
		ids := strings.Split(batch, ",")
		if entryID != "" {
			ids = []string{entryID}
		}
		got := make([]session.Entry, 0, len(ids))
		for _, id := range ids {
			at := slices.IndexFunc(visible, func(entry session.Entry) bool { return entry.ID == id })
			if at < 0 {
				http.Error(w, "entry not found in side conversation", http.StatusNotFound)
				return
			}
			got = append(got, visible[at])
		}
		if entryID != "" {
			writeJSON(w, http.StatusOK, map[string]any{"entry": got[0]})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"entries": got})
		}
		return
	}
	before, turnID := query.Get("before"), query.Get("turn")
	if before != "" && !entryIn(visible, before) {
		http.Error(w, "history cursor not found in side conversation", http.StatusConflict)
		return
	}
	if query.Get("view") == "trace" {
		rows := session.TraceWithContext(visible, leaf, session.TraceFilter{
			Types: splitQueryList(query.Get("type")), Roles: splitQueryList(query.Get("role")),
			Tools: splitQueryList(query.Get("tool")), FailedOnly: queryBool(query.Get("failed")),
			CacheMisses: queryBool(query.Get("cacheMiss")), Since: query.Get("since"), Until: query.Get("until"),
		}, 0)
		if before != "" {
			at := slices.IndexFunc(rows, func(row session.TraceEntry) bool { return row.ID == before })
			if at < 0 {
				http.Error(w, "trace cursor not found", http.StatusConflict)
				return
			}
			rows = rows[:at]
		}
		more := len(rows) > limit
		if more {
			rows = rows[len(rows)-limit:]
		}
		oldest := ""
		if len(rows) > 0 {
			oldest = rows[0].ID
		}
		writeJSON(w, http.StatusOK, map[string]any{"schemaVersion": 1, "id": snap.id, "leafId": leaf, "trace": rows, "hasMore": more, "oldestId": oldest})
		return
	}
	if query.Get("view") == "inspect" {
		body := s.sessionMapSnap(snap, map[string]any{"schemaVersion": 1, "leafId": leaf, "analysis": session.Analyze(visible, leaf), "children": []session.Info{}})
		writeJSON(w, http.StatusOK, body)
		return
	}
	fields := query.Get("fields")
	if hasField(fields, "index") && !hasField(fields, "runtime") {
		writeJSON(w, http.StatusOK, map[string]any{"id": snap.id, "index": session.BuildIndex(visible)})
		return
	}
	runtime := map[string]any{
		"leafId": leaf, "questions": []session.Question{},
		"queued": []session.QueuedItem{}, "extQueued": []session.ExtQueuedItem{},
		"runtime": map[string]any{"ready": true},
		"agents":  []any{}, "processes": []any{}, "commands": []any{},
		"availableSkills": []any{}, "availableExtensions": []any{}, "availableMCP": []any{},
	}
	queued, err := session.ReadQueue(snap.dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if queued != nil {
		runtime["queued"] = queued
	}
	projectionOnly := (hasField(fields, "runtime") || hasField(fields, "system")) &&
		!hasField(fields, "index") && before == "" && turnID == ""
	if hasField(fields, "system") {
		runtime["systemPrompt"] = snap.systemPrompt()
	}
	if projectionOnly {
		writeJSON(w, http.StatusOK, s.sessionMapSnap(snap, runtime))
		return
	}
	if query.Get("view") == "compact" {
		page := session.BuildCompact(visible, leaf, before, keep)
		if turnID != "" {
			var found bool
			page, found = session.BuildCompactTurn(visible, leaf, turnID, keep)
			if !found {
				http.Error(w, "turn not found", http.StatusNotFound)
				return
			}
		}
		if before != "" || turnID != "" {
			writeJSON(w, http.StatusOK, page)
			return
		}
		runtime["entries"], runtime["compactTurns"] = page.Entries, page.Turns
		runtime["hasMore"], runtime["oldestId"] = page.HasMore, page.OldestID
	} else {
		page := session.BuildTail(visible, leaf, limit, true)
		if turnID != "" {
			var found bool
			page, found = session.BuildTurn(visible, leaf, turnID, before, limit)
			if !found {
				http.Error(w, "turn not found", http.StatusNotFound)
				return
			}
		} else if before != "" {
			view := session.BuildBefore(visible, leaf, before, limit)
			page.Entries, page.HasMore, page.OldestID = view.Entries, view.HasMore, view.OldestID
		}
		if before != "" || turnID != "" {
			writeJSON(w, http.StatusOK, page)
			return
		}
		runtime["entries"], runtime["hasMore"], runtime["oldestId"] = page.Entries, page.HasMore, page.OldestID
	}
	if hasField(fields, "index") {
		runtime["index"] = session.BuildIndex(visible)
	}
	writeJSON(w, http.StatusOK, s.sessionMapSnap(snap, runtime))
}
