package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ki/internal/types"
)

func questionSession(t *testing.T) *Session {
	t.Helper()
	sess, err := Create(t.TempDir(), t.TempDir(), "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func questionMessage(t *testing.T, sess *Session, message types.Message) Entry {
	t.Helper()
	entry, err := sess.AppendMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestQuestionSnapshotStoredOnceAndExcludedFromMetadataCache(t *testing.T) {
	sess := questionSession(t)
	questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "batch"}}})
	inputs := make([]QuestionInput, 100)
	for i := range inputs {
		inputs[i].Title = "Choose scope"
	}
	resource := json.RawMessage(`{"private":"` + strings.Repeat("x", 1<<20) + `"}`)
	questions, err := sess.RequestQuestions("batch", inputs, resource)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(sess.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() > 2<<20 {
		t.Fatalf("resource duplicated in batch: %d bytes", before.Size())
	}
	q := questions[0]
	q.Status, q.Revision = "dismissed", q.Revision+1
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(sess.Dir, "events.jsonl"))
	if after.Size()-before.Size() > 4096 {
		t.Fatalf("update duplicated snapshot: %d bytes", after.Size()-before.Size())
	}
	transcript, err := ReadTranscript(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range transcript.Entries() {
		if entry.Details != nil {
			data, _ := json.Marshal(entry.Details)
			if strings.Contains(string(data), "snapshotResources") || strings.Contains(string(data), `"private"`) {
				t.Fatal("metadata cache retained private resource snapshot")
			}
		}
	}
	public, err := QuestionsAt(sess.Dir)
	if err != nil || len(public) != len(inputs) || len(public[0].SnapshotResources) != 0 {
		t.Fatalf("public projection retained snapshot: %v", err)
	}
	private, found := sess.Question(q.QuestionItemID)
	if !found || string(private.SnapshotResources) != string(resource) {
		t.Fatal("private snapshot cannot be hydrated after update")
	}
	metadata := map[string]any{"purpose": "question_side", "sourceSessionId": "parent", "questionSideResources": resource, "readOnlyWorkspaceRoot": "private", "sideInitialization": "ready"}
	publicMetadata := PublicMetadata(metadata)
	if len(publicMetadata) != 2 || publicMetadata["sourceSessionId"] != "parent" || len(metadata) != 5 {
		t.Fatalf("metadata sanitization = %+v", publicMetadata)
	}
}

func TestOversizedSidebandRejectedWithoutCorruptingSession(t *testing.T) {
	sess := questionSession(t)
	before, err := os.Stat(filepath.Join(sess.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendSidebandEvent(sess.Dir, "question_requested", map[string]any{"snapshot": strings.Repeat("x", maxJSONLLineBytes)}); err == nil {
		t.Fatal("oversized record accepted")
	}
	after, _ := os.Stat(filepath.Join(sess.Dir, "events.jsonl"))
	if before.Size() != after.Size() {
		t.Fatal("rejected event modified transcript")
	}
	if _, err := ReadTranscript(sess.Dir); err != nil {
		t.Fatalf("session unreadable after rejection: %v", err)
	}
}

func TestQuestionsFreezeSafeParallelBatchAndReplay(t *testing.T) {
	sess := questionSession(t)
	safe := questionMessage(t, sess, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "task"}}})
	source := questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{
		{Type: "toolCall", ID: "ask", Name: "request_user_input_async"},
		{Type: "toolCall", ID: "long", Name: "exec_command"},
	}})
	inputs := []QuestionInput{{Title: "Scope?", Options: []string{"local", "all"}}, {Title: "When?"}}
	qs, err := sess.RequestQuestions("ask", inputs, json.RawMessage(`{"version":1}`))
	if err != nil || len(qs) != 2 {
		t.Fatalf("request: %+v %v", qs, err)
	}
	for _, q := range qs {
		if q.SourceEntryID != source.ID || q.SnapshotEntryID != safe.ID || q.SupplementalUser != nil {
			t.Fatalf("unsafe or duplicate snapshot: %+v", q)
		}
	}
	questionMessage(t, sess, types.Message{Role: "toolResult", ToolCallID: "long"})
	questionMessage(t, sess, types.Message{Role: "toolResult", ToolCallID: "ask"})
	questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "later"}}})
	reopened, err := Open(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	restored := reopened.Questions()
	if len(restored) != 2 || restored[0].SnapshotEntryID != safe.ID || string(restored[0].SnapshotResources) != `{"version":1}` {
		t.Fatalf("snapshot drifted on reopen: %+v", restored)
	}
	duplicate, err := sess.RequestQuestions("ask", inputs)
	if err != nil || duplicate[0].QuestionItemID != qs[0].QuestionItemID {
		t.Fatalf("same source retry not idempotent: %+v %v", duplicate, err)
	}
	if _, err := sess.RequestQuestions("ask", []QuestionInput{{Title: "changed"}}); !errors.Is(err, ErrQuestionConflict) {
		t.Fatalf("changed retry silently accepted: %v", err)
	}
	questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "ask", Name: "request_user_input_async"}}})
	if _, err := sess.RequestQuestions("ask", inputs); !errors.Is(err, ErrQuestionConflict) {
		t.Fatalf("reused provider call id silently accepted: %v", err)
	}
}

func TestQuestionsRejectMissingSourceAndUnsafeProtocol(t *testing.T) {
	sess := questionSession(t)
	questionMessage(t, sess, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "task"}}})
	if _, err := sess.RequestQuestions("missing", []QuestionInput{{Title: "missing"}}); err == nil {
		t.Fatal("accepted a question without originating call")
	}
	for _, messages := range [][]types.Message{
		{{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "a"}}}, {Role: "assistant"}, {Role: "toolResult", ToolCallID: "a"}},
		{{Role: "toolResult", ToolCallID: "a"}},
		{{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "a"}, {Type: "toolCall", ID: "b"}}}, {Role: "toolResult", ToolCallID: "a"}},
	} {
		if protocolComplete(messages) {
			t.Fatalf("accepted incomplete/invalid protocol: %+v", messages)
		}
	}
}

func TestQuestionAnswerReceiptSurvivesBranchSwitchAndRestart(t *testing.T) {
	sess := questionSession(t)
	questionMessage(t, sess, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "task"}}})
	source := questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "ask", Name: "request_user_input_async"}}})
	qs, err := sess.RequestQuestions("ask", []QuestionInput{{Title: "Scope?"}})
	if err != nil {
		t.Fatal(err)
	}
	q := qs[0]
	q.Answer, q.SubmissionID, q.Status, q.Revision = "all", "submission", "answer_queued", q.Revision+1
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	if got, _ := sess.Question(q.QuestionItemID); got.Status != "answer_queued" {
		t.Fatal("receipt marked answered before user entry")
	}
	questionMessage(t, sess, types.Message{Role: "toolResult", ToolCallID: "ask"})
	branch := sess.LeafID()
	answer := questionMessage(t, sess, sess.QuestionAnswerMessage(q))
	got, _ := sess.Question(q.QuestionItemID)
	if got.Status != "answered" || got.AnswerEntryID != answer.ID || !got.AnswerBranchValid {
		t.Fatalf("answer not derived from typed user entry: %+v", got)
	}
	if err := sess.SetLeaf(branch); err != nil {
		t.Fatal(err)
	}
	questionMessage(t, sess, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "sibling"}}})
	got, _ = sess.Question(q.QuestionItemID)
	if got.Status != "answered" || !got.SourceBranchValid || got.AnswerBranchValid {
		t.Fatalf("global receipt/current branch conflated: %+v", got)
	}
	fresh, err := Open(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = fresh.Question(q.QuestionItemID)
	if got.SourceEntryID != source.ID || got.Status != "answered" || got.AnswerBranchValid {
		t.Fatalf("restart lost immutable receipt: %+v", got)
	}
}

func TestQuestionDoesNotTrustEnvelopeOrUnrelatedBranch(t *testing.T) {
	sess := questionSession(t)
	base := questionMessage(t, sess, types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "task"}}})
	questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "ask"}}})
	qs, err := sess.RequestQuestions("ask", []QuestionInput{{Title: "Q"}})
	if err != nil {
		t.Fatal(err)
	}
	q := qs[0]
	q.Status, q.Revision, q.Answer, q.SubmissionID = "answer_queued", q.Revision+1, "yes", "receipt"
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetLeaf(base.ID); err != nil {
		t.Fatal(err)
	}
	questionMessage(t, sess, sess.QuestionAnswerMessage(q))
	got, _ := sess.Question(q.QuestionItemID)
	if got.Status != "answer_queued" || got.SourceBranchValid {
		t.Fatalf("unrelated branch forged answer: %+v", got)
	}
}

func TestQuestionRevisionConcurrencyAndPrivateSnapshot(t *testing.T) {
	sess := questionSession(t)
	questionMessage(t, sess, types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "ask"}}})
	qs, err := sess.RequestQuestions("ask", []QuestionInput{{Title: "Q"}})
	if err != nil {
		t.Fatal(err)
	}
	q := qs[0]
	q.Status, q.Revision = "dismissed", q.Revision+1
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sess.UpdateQuestion(q); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrQuestionConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("CAS accepted %d writers", successes.Load())
	}
	q.SnapshotResources = json.RawMessage(`{"private":"snapshot"}`)
	q.SupplementalUser = &types.Message{Content: []types.Content{{Type: "text", Text: "private user"}}}
	q.SnapshotCWD = "private workspace"
	public, err := json.Marshal(q.Public())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(public, &fields)
	for _, name := range []string{"snapshotResources", "supplementalUser", "snapshotCwd"} {
		if _, exists := fields[name]; exists {
			t.Fatalf("public question exposed %s", name)
		}
	}
}
