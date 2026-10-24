package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/tool/builtin"
	"ki/internal/types"
)

func questionSideSeed(t *testing.T, s *Server, empty bool) (*session.Session, session.Question) {
	t.Helper()
	sess, err := session.Create(s.cfg.Sessions.Root, t.TempDir(), "frozen-provider", "frozen-model", "medium")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if !empty {
		if _, err := sess.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "original user task"}}}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = sess.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{
		{Type: "toolCall", ID: "ask", Name: "request_user_input_async"},
		{Type: "toolCall", ID: "long", Name: "exec_command"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(s.resources.Load(sess.ID(), sess.Header.CWD))
	if err != nil {
		t.Fatal(err)
	}
	questions, err := sess.RequestQuestions("ask", []session.QuestionInput{{Title: "Choose deployment scope", Options: []string{"staging", "production"}}}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	s.sidx.Add(sess.ID(), sess.Dir)
	return sess, questions[0]
}

func TestQuestionSideFrozenSnapshotAndMetadata(t *testing.T) {
	s, _ := testServer(t)
	source, q := questionSideSeed(t, s, false)
	if _, err := source.AppendMessage(types.Message{Role: "toolResult", ToolCallID: "long", Content: []types.Content{{Type: "text", Text: "late finding must not appear"}}}); err != nil {
		t.Fatal(err)
	}
	if err := source.SetModelAndThinking("new-provider", "new-model", "high"); err != nil {
		t.Fatal(err)
	}
	frozenCWD := source.Header.CWD
	source.Header.CWD = t.TempDir()
	side, err := s.createQuestionSide(source, q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = side.Close() }()
	if side.Header.CWD != frozenCWD || metadataString(side.Config.Metadata, "readOnlyWorkspaceRoot") != frozenCWD {
		t.Fatal("side followed later source workspace")
	}
	if side.Config.Provider != "new-provider" || side.Config.Model != "new-model" || side.Config.ThinkingEffort != "high" {
		t.Fatalf("side did not follow current parent model: %+v", side.Config)
	}
	if side.CacheAffinityKey() != source.CacheAffinityKey() {
		t.Fatal("lost source cache affinity")
	}
	if side.Header.ParentSession != source.ID() || side.Header.EffectiveForkMode() != session.ForkModeTree {
		t.Fatalf("missing lifecycle ownership: %+v", side.Header)
	}
	if len(side.Questions()) != 0 {
		t.Fatal("side inherited pending questions")
	}
	for _, message := range side.MessagesToLeaf() {
		if len(message.ToolCalls()) > 0 || strings.Contains(message.Text(), "late finding") {
			t.Fatalf("copied incomplete tool batch or late history: %+v", message)
		}
	}
	body := s.sessionMap(side, nil)
	if body["purpose"] != questionSidePurpose || body["sourceSessionId"] != source.ID() ||
		body["sourceQuestionItemId"] != q.QuestionItemID || body["sideBoundaryEntryId"] == "" {
		t.Fatalf("missing typed side metadata: %+v", body)
	}
	if visible := questionSideVisibleEntries(side.Entries(), body["sideBoundaryEntryId"].(string)); len(visible) != 0 {
		t.Fatalf("initial side display should be empty: %+v", visible)
	}
}

func TestQuestionSideRecoversUnlinkedAndPartialInitialization(t *testing.T) {
	s, _ := testServer(t)
	source, q := questionSideSeed(t, s, false)
	side, err := s.createQuestionSide(source, q)
	if err != nil {
		t.Fatal(err)
	}
	originalID := side.ID()
	_ = side.Close()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"purpose":"question_side","questionItemId":`+mustJSON(t, q.QuestionItemID)+`}`))
	r.SetPathValue("id", source.ID())
	w := httptest.NewRecorder()
	s.fork(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), originalID) {
		t.Fatalf("did not recover durable unlinked child: %d %s", w.Code, w.Body.String())
	}
	got, found := source.Question(q.QuestionItemID)
	if !found || got.SideSessionID != originalID {
		t.Fatal("recovery failed to link original child")
	}
	otherSource, otherQuestion := questionSideSeed(t, s, false)
	partial, err := session.CreateChildWithMetadata(s.cfg.Sessions.Root, otherSource, map[string]any{
		"purpose": questionSidePurpose, "sourceSessionId": otherSource.ID(), "sourceQuestionItemId": otherQuestion.QuestionItemID, "sideInitialization": "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	partialDir := partial.Dir
	_ = partial.Close()
	recovered, err := s.recoverQuestionSide(otherSource.ID(), otherQuestion.QuestionItemID)
	if err != nil || recovered != nil {
		t.Fatalf("partial child incorrectly recovered: %v", err)
	}
	if _, err := os.Stat(partialDir); !os.IsNotExist(err) {
		t.Fatal("partial initialization was not cleaned up")
	}
}

func TestQuestionSideCopiesSupplementalAttachment(t *testing.T) {
	s, _ := testServer(t)
	source, q := questionSideSeed(t, s, true)
	attachment := filepath.Join(source.Dir, "attachments", "user.txt")
	if err := os.MkdirAll(filepath.Dir(attachment), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attachment, []byte("original attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	q.SupplementalUser = &types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "attachment background", Path: attachment}}}
	side, err := s.createQuestionSide(source, q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = side.Close() }()
	messages := side.MessagesToLeaf()
	copied := filepath.Join(side.Dir, "attachments", "user.txt")
	if len(messages) != 2 || messages[0].Content[0].Path != copied || q.SupplementalUser.Content[0].Path != attachment {
		t.Fatalf("supplemental path not independently rewritten: %+v", messages)
	}
	if err := os.RemoveAll(source.Dir); err != nil {
		t.Fatal(err)
	}
	data, err := (questionSideReadOps{roots: questionSideRoots(side)}).ReadFile(t.Context(), copied)
	if err != nil || string(data) != "original attachment" {
		t.Fatalf("copied attachment unavailable without parent: %s %v", data, err)
	}
}

func TestQuestionSideParentModelPatchUpdatesExistingSide(t *testing.T) {
	s, hs := testServer(t)
	parent, questions := questionServerSeed(t, s, hs, 1)
	side, err := s.createQuestionSide(parent, questions[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = side.Close() }()
	code, body := postJSON(t, hs, http.MethodPatch, "/v1/sessions/"+parent.ID(), map[string]any{"model": "openai/gpt-5.6-terra"})
	if code != http.StatusOK {
		t.Fatalf("main model patch failed: %d %+v", code, body)
	}
	cfg, err := session.ReadConfig(side.Dir)
	if err != nil || cfg.Provider != "openai" || cfg.Model != "gpt-5.6-terra" {
		t.Fatalf("model picker did not update side: %+v %v", cfg, err)
	}
}

func TestQuestionSideFollowsParentModelAndDefersBusyChanges(t *testing.T) {
	s, _ := testServer(t)
	source, q := questionSideSeed(t, s, false)
	side, err := s.createQuestionSide(source, q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = side.Close() }()
	if err := source.SetModelAndThinking("changed-provider", "changed-model", "high"); err != nil {
		t.Fatal(err)
	}
	if err := s.syncQuestionSideModels(source); err != nil {
		t.Fatal(err)
	}
	cfg, err := session.ReadConfig(side.Dir)
	if err != nil || cfg.Provider != "changed-provider" || cfg.Model != "changed-model" || cfg.ThinkingEffort != "high" {
		t.Fatalf("idle side model did not follow: %+v %v", cfg, err)
	}
	s.sidx.Add(side.ID(), side.Dir)
	st, _, err := s.occupy(t.Context(), side.ID())
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	if err := source.SetModelAndThinking("next-provider", "next-model", "medium"); err != nil {
		t.Fatal(err)
	}
	if err := s.syncQuestionSideModels(source); err != nil {
		t.Fatal(err)
	}
	cfg, _ = session.ReadConfig(side.Dir)
	if cfg.Model != "changed-model" {
		t.Fatal("running side changed model during its active turn")
	}
	s.release(side.ID(), st)
	fresh, err := session.Open(side.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	if err := s.syncQuestionSideModel(fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Config.Provider != "next-provider" || fresh.Config.Model != "next-model" || fresh.Config.ThinkingEffort != "medium" {
		t.Fatalf("next side run did not follow model: %+v", fresh.Config)
	}
}

func TestQuestionSideEmptySnapshotDoesNotForkLatest(t *testing.T) {
	s, _ := testServer(t)
	source, q := questionSideSeed(t, s, true)
	if q.SnapshotEntryID != "" {
		t.Fatalf("expected empty safe prefix: %+v", q)
	}
	side, err := s.createQuestionSide(source, q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = side.Close() }()
	if len(side.MessagesToLeaf()) != 1 || len(side.MessagesToLeaf()[0].ToolCalls()) != 0 {
		t.Fatalf("empty snapshot copied live tools: %+v", side.MessagesToLeaf())
	}
}

func TestQuestionSideForkIsIdempotentAndGETOnlyShowsSuffix(t *testing.T) {
	s, _ := testServer(t)
	source, q := questionSideSeed(t, s, false)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetPathValue("id", source.ID())
		w := httptest.NewRecorder()
		s.fork(w, r)
		return w
	}
	body := `{"purpose":"question_side","questionItemId":` + mustJSON(t, q.QuestionItemID) + `}`
	first := request(http.MethodPost, "/", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first fork: %d %s", first.Code, first.Body.String())
	}
	var firstBody map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	second := request(http.MethodPost, "/", body)
	var secondBody map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if second.Code != http.StatusOK || firstBody["id"] != secondBody["id"] {
		t.Fatalf("fork did not reuse side: %s %s", first.Body.String(), second.Body.String())
	}
	listResponse := httptest.NewRecorder()
	s.list(listResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), firstBody["id"].(string)) || strings.Contains(listResponse.Body.String(), "questionSideResources") {
		t.Fatalf("public listing lost ready side or exposed snapshot: %d %s", listResponse.Code, listResponse.Body.String())
	}
	side, err := s.open(firstBody["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = side.Close() }()
	visible, err := side.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "explain staging"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?view=compact", "?fields=index", "?view=trace", "?view=inspect"} {
		r := httptest.NewRequest(http.MethodGet, "/"+query, nil)
		r.SetPathValue("id", side.ID())
		w := httptest.NewRecorder()
		s.get(w, r)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "original user task") {
			t.Fatalf("side GET %s leaked inherited history or failed: %d %s", query, w.Code, w.Body.String())
		}
		if query == "" && !strings.Contains(w.Body.String(), visible.ID) {
			t.Fatalf("side GET omitted side message: %s", w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/?entry="+q.SnapshotEntryID, nil)
	r.SetPathValue("id", side.ID())
	w := httptest.NewRecorder()
	s.get(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("inherited entry lookup should be hidden: %d %s", w.Code, w.Body.String())
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestQuestionSideToolsReadonlyAndPathBounded(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(root, "allowed")
	if err := os.WriteFile(allowed, []byte("readable"), 0600); err != nil {
		t.Fatal(err)
	}
	sess := &session.Session{Dir: t.TempDir(), Header: session.Header{CWD: root}, Config: session.Config{Metadata: map[string]any{"purpose": questionSidePurpose, "readOnlyWorkspaceRoot": root}}}
	tools := restrictQuestionSideTools(sess, (builtin.Set{
		CWD: root, ReadOps: questionSideReadOps{roots: []string{root}},
	}).Build(builtin.Profile{}))
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"glob", "grep", "read"}) {
		t.Fatalf("unexpected allowed tools: %v", names)
	}
	ctx := t.Context()
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"read", map[string]any{"file_path": secret}},
		{"grep", map[string]any{"pattern": ".", "path": filepath.Dir(secret)}},
		{"glob", map[string]any{"pattern": filepath.Dir(secret) + "/*", "path": root}},
		{"glob", map[string]any{"pattern": "../**", "path": root}},
	} {
		for _, tool := range tools {
			if tool.Name() == test.name && !tool.Execute(ctx, test.args).IsError {
				t.Fatalf("%s allowed escaped path: %+v", test.name, test.args)
			}
		}
	}
	for _, tool := range tools {
		if tool.Name() == "read" && tool.Execute(ctx, map[string]any{"file_path": allowed}).IsError {
			t.Fatal("read rejected allowed workspace file")
		}
	}
	if err := os.Symlink(secret, filepath.Join(root, "escape")); err == nil {
		for _, tool := range tools {
			if !tool.Execute(ctx, map[string]any{"file_path": filepath.Join(root, "escape"), "path": filepath.Join(root, "escape"), "pattern": "."}).IsError {
				t.Fatalf("%s followed outside symlink", tool.Name())
			}
		}
	}
}

func TestQuestionSideRunOnlyAdvertisesReadonlyTools(t *testing.T) {
	var seen loop.Request
	s := codeModeTestServer(t, codeModeServerStreamer(func(_ context.Context, req loop.Request) (types.Message, error) {
		seen = req
		return codeModeStop(), nil
	}))
	id := codeModeTestSession(t, s, t.TempDir(), "completions")
	sess, err := s.open(id)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := sess.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "Side-conversation boundary"}}})
	if err != nil {
		t.Fatal(err)
	}
	parentID := codeModeTestSession(t, s, sess.Header.CWD, "completions")
	sess.Config.Metadata = map[string]any{"purpose": questionSidePurpose, "sourceSessionId": parentID, "sideInitialization": "ready", "sideBoundaryEntryId": boundary.ID, "readOnlyWorkspaceRoot": sess.Header.CWD, "questionSideResources": s.resources.Load(id, sess.Header.CWD)}
	if err := sess.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()
	codeModeRunPrompt(t, s, id, "help me understand this choice")
	var names []string
	for _, tool := range seen.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"glob", "grep", "read"}) {
		t.Fatalf("run tools not readonly: %+v", names)
	}
	if !strings.Contains(seen.System, questionSidePolicy) {
		t.Fatal("side high-priority policy missing")
	}
}
