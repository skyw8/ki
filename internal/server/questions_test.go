package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

func questionServerSeed(t *testing.T, s *Server, hs *httptest.Server, count int) (*session.Session, []session.Question) {
	t.Helper()
	id := createSession(t, hs, t.TempDir())
	sess, err := s.open(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(types.Message{Role: "user", Content: []types.Content{{Type: "text", Text: "choose scope"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "ask", Name: "request_user_input_async"}}}); err != nil {
		t.Fatal(err)
	}
	inputs := make([]session.QuestionInput, count)
	for i := range inputs {
		inputs[i] = session.QuestionInput{Title: "Scope?", Options: []string{"local", "all"}}
	}
	if err := s.requestQuestions(sess, "ask", inputs); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(types.Message{Role: "toolResult", ToolCallID: "ask", Content: []types.Content{{Type: "text", Text: `{"accepted":true}`}}}); err != nil {
		t.Fatal(err)
	}
	return sess, sess.Questions()
}

func waitQuestionRun(t *testing.T, s *Server, id string) {
	t.Helper()
	st := s.runAt(id)
	if st == nil {
		t.Fatal("missing run")
	}
	select {
	case <-st.released:
	case <-time.After(5 * time.Second):
		t.Fatal("question run did not finish")
	}
}

func TestQuestionListLinksBeforeSideCreationAndAfterUpdate(t *testing.T) {
	s, hs := testServer(t)
	sess, qs := questionServerSeed(t, s, hs, 1)
	defer func() { _ = sess.Close() }()
	read := func(etag string) (*httptest.ResponseRecorder, []map[string]any) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		s.list(w, r)
		var rows []map[string]any
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
		}
		return w, rows
	}
	settled := func(etag string) (*httptest.ResponseRecorder, []map[string]any) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			w, rows := read(etag)
			if len(rows) == 1 && rows[0]["questionLinksLoading"] != true {
				return w, rows
			}
			if time.Now().After(deadline) {
				t.Fatalf("list summary did not settle: %s", w.Body.String())
			}
			time.Sleep(time.Millisecond)
		}
	}
	first, rows := settled("")
	if len(rows) != 1 {
		t.Fatalf("unexpected listing: %s", first.Body.String())
	}
	links := rows[0]["questionLinks"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["questionItemId"] != qs[0].QuestionItemID || links[0].(map[string]any)["sideSessionId"] != "" {
		t.Fatalf("missing lazy discussion: %s", first.Body.String())
	}
	if len(links[0].(map[string]any)) != 5 {
		t.Fatalf("list links must contain navigation fields only: %+v", links)
	}
	q := qs[0]
	q.Status = "dismissed"
	q.Revision++
	if err := sess.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}
	second, rows := settled(first.Header().Get("ETag"))
	if second.Code != http.StatusOK || second.Header().Get("ETag") == first.Header().Get("ETag") || rows[0]["questionLinks"].([]any)[0].(map[string]any)["status"] != "dismissed" {
		t.Fatalf("question update not reflected in conditional list: %d %s", second.Code, second.Body.String())
	}
}

func TestQuestionReplyIdleStartsExactlyOnceAndGETProjection(t *testing.T) {
	s, hs := testServer(t)
	sess, qs := questionServerSeed(t, s, hs, 1)
	q := qs[0]
	body := map[string]any{"questionReply": questionReplyRequest{QuestionItemID: q.QuestionItemID, Revision: q.Revision, SubmissionID: "answer-1", Answer: "all"}}
	code, out := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+sess.ID()+"/prompt", body)
	if code != http.StatusAccepted || out["accepted"] != true {
		t.Fatalf("submit %d %+v", code, out)
	}
	waitQuestionRun(t, s, sess.ID())
	q, ok := sess.Question(q.QuestionItemID)
	if !ok || q.Status != "answered" || q.AnswerEntryID == "" || !q.AnswerBranchValid {
		t.Fatalf("answer not committed: %+v", q)
	}
	code, out = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+sess.ID()+"/prompt", body)
	if code != http.StatusOK {
		t.Fatalf("retry %d %+v", code, out)
	}
	fresh, err := s.open(sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range fresh.Entries() {
		if entry.Message != nil && entry.Message.QuestionReply != nil {
			count++
			if entry.Message.QuestionReply.Answer != "all" || !strings.Contains(entry.Message.Text(), "<send_user_message_question_reply>") {
				t.Fatalf("missing canonical answer and UI metadata: %+v", entry.Message)
			}
		}
	}
	if count != 1 {
		t.Fatalf("answer committed %d times", count)
	}
	for _, query := range []string{"?view=compact&keep=0", "?fields=runtime"} {
		code, projection := postJSON(t, hs, http.MethodGet, "/v1/sessions/"+sess.ID()+query, nil)
		questions, _ := projection["questions"].([]any)
		if code != http.StatusOK || len(questions) != 1 || questions[0].(map[string]any)["status"] != "answered" {
			t.Fatalf("missing complete question projection %s: %d %+v", query, code, projection)
		}
		raw, _ := json.Marshal(questions)
		for _, private := range []string{"snapshotResources", "supplementalUser", "snapshotCwd"} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("public projection leaked %s", private)
			}
		}
	}
}

func TestQuestionReplyBusyIsDurableIdempotentAndDoesNotReviveStop(t *testing.T) {
	s, hs := testServer(t)
	sess, qs := questionServerSeed(t, s, hs, 2)
	st, _, err := s.occupy(context.Background(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	released := false
	defer func() {
		if !released {
			s.release(sess.ID(), st)
		}
	}()
	reply := func(q session.Question, id, answer string) int {
		code, _ := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+sess.ID()+"/prompt", map[string]any{
			"questionReply": questionReplyRequest{QuestionItemID: q.QuestionItemID, Revision: q.Revision, SubmissionID: id, Answer: answer},
		})
		return code
	}
	if code := reply(qs[0], "same-submission", "all"); code != http.StatusAccepted {
		t.Fatalf("busy reply %d", code)
	}
	if code := reply(qs[0], "same-submission", "all"); code != http.StatusOK {
		t.Fatalf("duplicate reply %d", code)
	}
	if code := reply(qs[1], "same-submission", "local"); code != http.StatusConflict {
		t.Fatalf("cross-question submission identity collision accepted: %d", code)
	}
	st.mu.Lock()
	pending := st.inbox.TakeWork()
	st.mu.Unlock()
	if len(pending) != 1 || pending[0].QuestionReply == nil {
		t.Fatalf("duplicate/missing inbox answer: %+v", pending)
	}
	st.inbox.Push(pending[0])
	s.cancelRun(sess.ID(), st, cancelReasonUserRequest, "user", false)
	s.preserveRunInbox(sess.ID(), st)
	s.release(sess.ID(), st)
	released = true
	if s.running(sess.ID()) {
		t.Fatal("stopped task was revived by answer ledger")
	}
	queue, err := session.ReadQueue(sess.Dir)
	if err != nil || len(queue) != 0 {
		t.Fatalf("question receipt leaked into auto-start queue: %+v %v", queue, err)
	}
	q, _ := sess.Question(qs[0].QuestionItemID)
	if q.Status != "answer_queued" {
		t.Fatalf("unconsumed answer became answered: %+v", q)
	}
	code, out := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+sess.ID()+"/question-action",
		map[string]any{"questionItemId": q.QuestionItemID, "revision": q.Revision, "action": "continue"})
	if code != http.StatusAccepted || out["delivery"] != "started" {
		t.Fatalf("explicit continue failed: %d %+v", code, out)
	}
	waitQuestionRun(t, s, sess.ID())
	q, _ = sess.Question(q.QuestionItemID)
	if q.Status != "answered" {
		t.Fatalf("continue failed to deliver durable answer: %+v", q)
	}
}

func TestQuestionReplyClosedInboxRecoversOnExplicitNormalPrompt(t *testing.T) {
	s, hs := testServer(t)
	sess, questions := questionServerSeed(t, s, hs, 1)
	st, _, err := s.occupy(context.Background(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	st.mu.Lock()
	st.steerClosed = true
	st.mu.Unlock()
	q := questions[0]
	code, out := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+sess.ID()+"/prompt", map[string]any{
		"questionReply": questionReplyRequest{QuestionItemID: q.QuestionItemID, Revision: q.Revision, SubmissionID: "closed-inbox", Answer: "all"},
	})
	if code != http.StatusAccepted || out["delivery"] != "queued" {
		t.Fatalf("close race receipt: %d %+v", code, out)
	}
	s.release(sess.ID(), st)
	if s.running(sess.ID()) {
		t.Fatal("closed run automatically revived")
	}
	code, _ = postJSON(t, hs, http.MethodPost, "/v1/sessions/"+sess.ID()+"/prompt", map[string]any{"text": "continue the task"})
	if code != http.StatusAccepted {
		t.Fatalf("explicit normal prompt failed: %d", code)
	}
	waitQuestionRun(t, s, sess.ID())
	q, _ = sess.Question(q.QuestionItemID)
	if q.Status != "answered" {
		t.Fatalf("normal startup failed to restore durable answer: %+v", q)
	}
	entries, err := session.AllEntries(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	answers := 0
	for _, entry := range entries {
		if entry.Message != nil && entry.Message.QuestionReply != nil {
			answers++
		}
		if entry.Type != "question_requested" {
			continue
		}
		r := httptest.NewRequest(http.MethodGet, "/?entry="+entry.ID, nil)
		r.SetPathValue("id", sess.ID())
		w := httptest.NewRecorder()
		s.get(w, r)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "snapshotResources") || strings.Contains(w.Body.String(), "supplementalUser") {
			t.Fatalf("entry hydration exposed private snapshot: %d %s", w.Code, w.Body.String())
		}
	}
	if answers != 1 {
		t.Fatalf("restored answer delivered %d times", answers)
	}
}

func TestQuestionReplyRaceAndBranchValidation(t *testing.T) {
	s, hs := testServer(t)
	sess, qs := questionServerSeed(t, s, hs, 1)
	q := qs[0]
	st, _, err := s.occupy(context.Background(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	defer s.release(sess.ID(), st)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, submission := range []string{"left", "right"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+sess.ID()+"/prompt", map[string]any{
				"questionReply": questionReplyRequest{QuestionItemID: q.QuestionItemID, Revision: q.Revision, SubmissionID: submission, Answer: "all"},
			})
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	accepted, conflicts := 0, 0
	for code := range codes {
		if code == http.StatusAccepted {
			accepted++
		} else if code == http.StatusConflict {
			conflicts++
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("first-wins answer race: accepted=%d conflicts=%d", accepted, conflicts)
	}
}

func TestQuestionRequestedSSEAndDurableReplay(t *testing.T) {
	s, hs := testServer(t)
	sess, qs := questionServerSeed(t, s, hs, 1)
	if len(qs) != 1 {
		t.Fatal("missing durable question")
	}
	st, _, err := s.occupy(context.Background(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer s.release(sess.ID(), st)
	if err := s.requestQuestions(sess, "ask", []session.QuestionInput{{Title: qs[0].Title, Options: qs[0].Options}}); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	requested, updated := false, false
	for _, ev := range st.evs {
		if ev.Type == loop.QuestionRequested && len(ev.Questions) == 1 {
			requested = true
		}
		if ev.Type == loop.QuestionUpdated && ev.Question != nil && ev.Question.QuestionItemID == qs[0].QuestionItemID {
			updated = true
		}
	}
	st.mu.Unlock()
	if !requested || !updated {
		t.Fatal("question fact not projected to SSE events")
	}
	fresh, err := session.Open(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got := fresh.Questions()
	if len(got) != 1 || got[0].QuestionItemID != qs[0].QuestionItemID {
		t.Fatalf("restart lost question facts: %+v", got)
	}
}
