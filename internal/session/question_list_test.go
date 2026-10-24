package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ki/internal/state"
	"ki/internal/types"
)

func questionListSession(t *testing.T) (*Session, string) {
	t.Helper()
	root := t.TempDir()
	sess, err := Create(root, t.TempDir(), "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	questionMessage(t, sess, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "private ordinary message"}}})
	return sess, root
}

func questionListRequest(t *testing.T, sess *Session, call string) Question {
	t.Helper()
	questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: call}}})
	qs, err := sess.RequestQuestions(call, []QuestionInput{{Title: "Choose scope", Options: []string{"private-option"}}}, json.RawMessage(`{"resource":"private-resource"}`))
	if err != nil {
		t.Fatal(err)
	}
	return qs[0]
}

func questionListBuild(t *testing.T, sess *Session, prior *questionListSummary) *questionListSummary {
	t.Helper()
	summary, err := buildQuestionListSummary(sess.Dir, prior, nil)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func questionListAssertProjection(t *testing.T, sess *Session, summary *questionListSummary) []QuestionLink {
	t.Helper()
	qs, err := QuestionsAt(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var want []QuestionLink
	for _, q := range qs {
		if q.SourceBranchValid || q.SideSessionID != "" {
			want = append(want, QuestionLink{QuestionItemID: q.QuestionItemID, Title: q.Title, Status: q.Status, Revision: q.Revision, SideSessionID: q.SideSessionID})
		}
	}
	cfg, err := ReadConfig(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got := summary.links(cfg.ActiveLeafID)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary links = %+v, QuestionsAt links = %+v", got, want)
	}
	return got
}

func TestQuestionListColdAsyncPersistedAndFreshCache(t *testing.T) {
	sess, root := questionListSession(t)
	q := questionListRequest(t, sess, "cold")
	transcriptCaches.drop(filepath.Clean(sess.Dir))
	c := NewListCache()
	t.Cleanup(c.Close)
	c.workers <- struct{}{}
	c.workers <- struct{}{}
	blocked := true
	defer func() {
		if blocked {
			<-c.workers
			<-c.workers
		}
	}()
	changed := make(chan struct{}, 1)
	c.SetChangeHandler(func() { changed <- struct{}{} })
	rows, err := c.List(root)
	if err != nil || len(rows) != 1 || !rows[0].QuestionLinksLoading || len(rows[0].QuestionLinks) != 0 {
		t.Fatalf("cold list = %+v, %v", rows, err)
	}
	transcriptCaches.mu.Lock()
	_, loaded := transcriptCaches.items[filepath.Clean(sess.Dir)]
	transcriptCaches.mu.Unlock()
	if loaded {
		t.Fatal("cold list loaded transcript while summary workers were blocked")
	}
	<-c.workers
	<-c.workers
	blocked = false
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("summary builder did not publish completion")
	}
	rows, err = c.List(root)
	if err != nil || len(rows) != 1 || rows[0].QuestionLinksLoading || len(rows[0].QuestionLinks) != 1 || rows[0].QuestionLinks[0].QuestionItemID != q.QuestionItemID {
		t.Fatalf("settled list = %+v, %v", rows, err)
	}
	if summary := readQuestionListSummary(sess.Dir); summary == nil {
		t.Fatal("settled summary was not persisted")
	}
	fresh := NewListCache()
	t.Cleanup(fresh.Close)
	rows, err = fresh.List(root)
	if err != nil || len(rows) != 1 || rows[0].QuestionLinksLoading || !reflect.DeepEqual(rows[0].QuestionLinks, c.rows[sess.Dir].summary.links("")) {
		t.Fatalf("fresh cache did not immediately reuse persisted summary: %+v, %v", rows, err)
	}
}

func TestQuestionListIncrementalPrivacyAndReceiptBranches(t *testing.T) {
	sess, _ := questionListSession(t)
	base := sess.LeafID()
	q := questionListRequest(t, sess, "incremental")
	first := questionListBuild(t, sess, nil)
	questionListAssertProjection(t, sess, first)
	before, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	q.Answer, q.SubmissionID, q.Status, q.Revision = "private-answer-body", "submission", "answer_queued", q.Revision+1
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	questionMessage(t, sess, types.Message{Role: "toolResult", ToolCallID: "incremental"})
	branch := sess.LeafID()
	queued := questionListBuild(t, sess, first)
	if links := questionListAssertProjection(t, sess, queued); len(links) != 1 || links[0].Status != "answer_queued" {
		t.Fatalf("queued receipt = %+v", links)
	}
	answer := questionMessage(t, sess, sess.QuestionAnswerMessage(q))
	answered := questionListBuild(t, sess, queued)
	if links := questionListAssertProjection(t, sess, answered); len(links) != 1 || links[0].Status != "answered" {
		t.Fatalf("typed answer receipt = %+v", links)
	}
	if answered.Offset <= first.Offset || answered.Offset != answered.Size || len(answered.Nodes) <= len(first.Nodes) {
		t.Fatalf("append did not advance summary: first=%+v next=%+v", first, answered)
	}
	after, _ := json.Marshal(first)
	if !bytes.Equal(before, after) {
		t.Fatal("incremental build mutated prior snapshot")
	}
	if err := sess.SetLeaf(branch); err != nil {
		t.Fatal(err)
	}
	questionMessage(t, sess, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "private sibling body"}}})
	sibling := questionListBuild(t, sess, answered)
	if links := questionListAssertProjection(t, sess, sibling); len(links) != 1 || links[0].Status != "answered" {
		t.Fatalf("sibling lost global receipt: %+v", links)
	}
	if err := sess.SetLeaf(base); err != nil {
		t.Fatal(err)
	}
	if links := questionListAssertProjection(t, sess, sibling); len(links) != 0 {
		t.Fatalf("question outside source branch remained visible: %+v", links)
	}
	q, found := sess.Question(q.QuestionItemID)
	if !found {
		t.Fatal("question disappeared after branch switch")
	}
	q.SideSessionID, q.Revision = "side-session", q.Revision+1
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	side := questionListBuild(t, sess, sibling)
	if links := questionListAssertProjection(t, sess, side); len(links) != 1 || links[0].Status != "answered" {
		t.Fatalf("persistent side lost receipt outside source branch: %+v", links)
	}
	if err := sess.SetLeaf(answer.ID); err != nil {
		t.Fatal(err)
	}
	questionListAssertProjection(t, sess, side)
	raw, err := os.ReadFile(questionListPath(sess.Dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-answer-body", "private ordinary message", "private sibling body", "private-option", "private-resource", "snapshotResources", "supplementalUser", "snapshotCwd"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("summary persisted private content %q", private)
		}
	}
	if !bytes.Contains(raw, []byte(questionListHash(q.Answer))) {
		t.Fatal("summary omitted answer hash needed for receipt identity")
	}
}

func TestQuestionListRejectsUnrelatedTypedAnswer(t *testing.T) {
	sess, _ := questionListSession(t)
	base := sess.LeafID()
	q := questionListRequest(t, sess, "unrelated")
	q.Answer, q.SubmissionID, q.Status, q.Revision = "accepted answer", "accepted-submission", "answer_queued", q.Revision+1
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetLeaf(base); err != nil {
		t.Fatal(err)
	}
	questionMessage(t, sess, sess.QuestionAnswerMessage(q))
	if err := sess.SetLeaf(q.SourceEntryID); err != nil {
		t.Fatal(err)
	}
	forged := sess.QuestionAnswerMessage(q)
	forged.QuestionReply.Answer = "conflicting answer"
	questionMessage(t, sess, forged)
	summary := questionListBuild(t, sess, nil)
	if links := questionListAssertProjection(t, sess, summary); len(links) != 1 || links[0].Status != "answer_queued" {
		t.Fatalf("unrelated or mismatched answer consumed receipt: %+v", links)
	}
}

func TestQuestionListPartialAppendAndChangedAnchor(t *testing.T) {
	sess, _ := questionListSession(t)
	questionListRequest(t, sess, "anchor")
	prior := questionListBuild(t, sess, nil)
	path := entriesPath(sess.Dir)
	appendBytes := func(raw []byte) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(raw); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	appendBytes([]byte("\n  \n"))
	prior = questionListBuild(t, sess, prior)
	appendBytes([]byte(`{"type":"message","id":"partial","parentId":"` + sess.LeafID()))
	partial := questionListBuild(t, sess, prior)
	if partial.Offset != prior.Offset || !reflect.DeepEqual(partial.Facts, prior.Facts) {
		t.Fatal("incomplete append advanced committed summary")
	}
	appendBytes([]byte("\",\"message\":{\"role\":\"user\"}}\n"))
	complete := questionListBuild(t, sess, partial)
	if complete.Offset != complete.Size || len(complete.Nodes) != len(prior.Nodes)+1 {
		t.Fatal("completed append was skipped or duplicated")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("Choose scope"), []byte("Replaced scope"))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rebuilt := questionListBuild(t, sess, complete)
	if links := questionListAssertProjection(t, sess, rebuilt); len(links) != 1 || links[0].Title != "Replaced scope" {
		t.Fatalf("changed history anchor retained stale facts: %+v", links)
	}
}

func TestQuestionListCacheCorruptNewerAndTruncation(t *testing.T) {
	for _, mode := range []string{"corrupt", "newer", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			sess, _ := questionListSession(t)
			prefix, err := os.ReadFile(entriesPath(sess.Dir))
			if err != nil {
				t.Fatal(err)
			}
			questionListRequest(t, sess, mode)
			prior := questionListBuild(t, sess, nil)
			switch mode {
			case "corrupt":
				if err := os.WriteFile(questionListPath(sess.Dir), []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
				if readQuestionListSummary(sess.Dir) != nil {
					t.Fatal("corrupt summary accepted")
				}
				questionListAssertProjection(t, sess, questionListBuild(t, sess, nil))
				if readQuestionListSummary(sess.Dir) == nil {
					t.Fatal("corrupt summary not replaced")
				}
			case "newer":
				future := map[string]any{"version": questionListVersion + 1, "future": "preserve"}
				if err := state.WriteVersioned(questionListPath(sess.Dir), questionListVersion+1, future, 0o600); err != nil {
					t.Fatal(err)
				}
				before, _ := os.ReadFile(questionListPath(sess.Dir))
				if readQuestionListSummary(sess.Dir) != nil {
					t.Fatal("newer summary accepted")
				}
				questionListAssertProjection(t, sess, questionListBuild(t, sess, nil))
				after, _ := os.ReadFile(questionListPath(sess.Dir))
				if !bytes.Equal(before, after) {
					t.Fatal("newer summary overwritten")
				}
			case "truncated":
				if err := os.WriteFile(entriesPath(sess.Dir), prefix, 0o600); err != nil {
					t.Fatal(err)
				}
				next := questionListBuild(t, sess, prior)
				if next.Offset != int64(len(prefix)) || len(next.Facts) != 0 || len(next.links("")) != 0 {
					t.Fatalf("truncated history retained old facts: %+v", next)
				}
			}
		})
	}
}
