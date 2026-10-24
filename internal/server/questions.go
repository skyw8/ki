package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/tool/builtin/catalog"
	"ki/internal/types"
)

var serverQuestionGates sync.Map

func (s *Server) questionGate(id string) *sync.Mutex {
	key := s.cfg.Sessions.Root + "\x00" + id
	gate, _ := serverQuestionGates.LoadOrStore(key, &sync.Mutex{})
	return gate.(*sync.Mutex)
}

type questionReplyRequest struct {
	QuestionItemID string                      `json:"questionItemId"`
	Revision       int64                       `json:"revision"`
	SubmissionID   string                      `json:"submissionId"`
	Answer         string                      `json:"answer"`
	AnswerForm     *session.QuestionAnswerForm `json:"answerForm,omitempty"`
}

func (s *Server) questionEligible(sess *session.Session) bool {
	if sess.Config.Metadata["purpose"] == "question_side" {
		return false
	}
	if s.agentTasks != nil {
		_, child := s.agentTasks.TaskForSession(sess.ID())
		return !child
	}
	return true
}

func (s *Server) requestQuestions(sess *session.Session, callID string, inputs []session.QuestionInput) error {
	gate := s.questionGate(sess.ID())
	gate.Lock()
	defer gate.Unlock()
	if !s.questionEligible(sess) {
		return errors.New("request_user_input_async is only available in main sessions")
	}
	snapshot, err := json.Marshal(s.resources.Load(sess.ID(), sess.Header.CWD))
	if err != nil {
		return err
	}
	questions, err := sess.RequestQuestions(callID, inputs, snapshot)
	if err != nil {
		return err
	}
	for i := range questions {
		questions[i] = questions[i].Public()
	}
	s.publishSideband(sess.ID(), loop.Event{Type: loop.QuestionRequested, Questions: questions}, false)
	for i := range questions {
		s.publishQuestion(sess.ID(), questions[i])
	}
	return nil
}

func (s *Server) publishQuestion(id string, q session.Question) {
	q = q.Public()
	s.publishSideband(id, loop.Event{Type: loop.QuestionUpdated, Question: &q, Timestamp: time.Now().UnixMilli()}, false)
}

func (s *Server) questionAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		QuestionItemID string `json:"questionItemId"`
		Revision       int64  `json:"revision"`
		Action         string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Action == "continue" {
		s.continueQuestionReply(w, r, body.QuestionItemID, body.Revision)
		return
	}
	if body.Action != "dismiss" && body.Action != "restore" {
		http.Error(w, "action must be dismiss, restore or continue", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	gate := s.questionGate(id)
	gate.Lock()
	defer gate.Unlock()
	sess, err := s.open(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	q, ok := sess.Question(body.QuestionItemID)
	if !ok {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	if q.Revision != body.Revision || (q.Status != "pending" && q.Status != "dismissed") {
		http.Error(w, "question revision or status conflict", http.StatusConflict)
		return
	}
	next := "dismissed"
	if body.Action == "restore" {
		next = "pending"
	}
	if q.Status != next {
		q.Status, q.Revision = next, q.Revision+1
		if err := sess.UpdateQuestion(q); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		q, _ = sess.Question(q.QuestionItemID)
		s.publishQuestion(id, q)
	}
	writeJSON(w, http.StatusOK, map[string]any{"question": q.Public()})
}

func (s *Server) submitQuestionReply(w http.ResponseWriter, r *http.Request, body questionReplyRequest) {
	id := r.PathValue("id")
	if strings.TrimSpace(body.QuestionItemID) == "" || strings.TrimSpace(body.SubmissionID) == "" ||
		len(body.SubmissionID) > 256 || strings.TrimSpace(body.Answer) == "" || len(body.Answer) > 64*1024 {
		http.Error(w, "questionReply requires questionItemId, submissionId and a nonblank answer (maximum 64KiB)", http.StatusBadRequest)
		return
	}
	inputGate := s.inputGate(id)
	inputGate.Lock()
	defer inputGate.Unlock()
	if err := s.sessionAdmissionError(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	gate := s.questionGate(id)
	gate.Lock()
	defer gate.Unlock()
	sess, err := s.open(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	q, ok := sess.Question(body.QuestionItemID)
	if !ok {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	if body.AnswerForm != nil {
		form := body.AnswerForm
		if form.Selected != "" && !slices.Contains(q.Options, form.Selected) {
			http.Error(w, "answerForm selected must be an original question option", http.StatusBadRequest)
			return
		}
		parts := []string{}
		for _, value := range []string{form.Selected, form.Text} {
			if value = strings.TrimSpace(value); value != "" {
				parts = append(parts, value)
			}
		}
		if strings.Join(parts, "\n\n") != body.Answer {
			http.Error(w, "answerForm must match answer", http.StatusBadRequest)
			return
		}
	}
	if q.SubmissionID != "" {
		if q.SubmissionID != body.SubmissionID || q.Answer != body.Answer ||
			(q.AnswerForm != nil && body.AnswerForm != nil && *q.AnswerForm != *body.AnswerForm) {
			http.Error(w, "question already has another answer", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "session_id": id, "question": q.Public(), "clientRequestId": q.SubmissionID})
		return
	}
	if q.Revision != body.Revision || !q.SourceBranchValid || (q.Status != "pending" && q.Status != "dismissed") {
		http.Error(w, "question revision, status or source branch conflict", http.StatusConflict)
		return
	}
	for _, other := range sess.Questions() {
		if other.QuestionItemID != q.QuestionItemID && other.SubmissionID == body.SubmissionID {
			http.Error(w, "submissionId already belongs to another question", http.StatusConflict)
			return
		}
	}
	ref, _, err := s.resolveSessionModel(sess, "")
	if err == nil && s.requireModelCredential {
		_, _, _, err = s.registry.Resolve(ref.Provider, ref.Model)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	q.Answer, q.AnswerForm, q.SubmissionID, q.Status, q.Revision = body.Answer, body.AnswerForm, body.SubmissionID, "answer_queued", q.Revision+1
	if err := sess.UpdateQuestion(q); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	q, _ = sess.Question(q.QuestionItemID)
	s.publishQuestion(id, q)
	s.deliverQuestionAnswer(w, r, sess, q)
}

func (s *Server) continueQuestionReply(w http.ResponseWriter, r *http.Request, questionItemID string, revision int64) {
	id := r.PathValue("id")
	inputGate := s.inputGate(id)
	inputGate.Lock()
	defer inputGate.Unlock()
	if err := s.sessionAdmissionError(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	gate := s.questionGate(id)
	gate.Lock()
	defer gate.Unlock()
	sess, err := s.open(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()
	q, ok := sess.Question(questionItemID)
	if !ok {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	if q.Revision != revision || q.Status != "answer_queued" || !q.SourceBranchValid {
		http.Error(w, "question revision, status or source branch conflict", http.StatusConflict)
		return
	}
	s.deliverQuestionAnswer(w, r, sess, q)
}

func (s *Server) deliverQuestionAnswer(w http.ResponseWriter, r *http.Request, sess *session.Session, q session.Question) {
	id := sess.ID()
	message := sess.QuestionAnswerMessage(q)
	live := s.runAt(id)
	if s.pushQuestionAnswer(live, message) {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "delivery": "steered", "session_id": id, "question": q.Public(), "clientRequestId": q.SubmissionID})
		return
	}
	if s.running(id) {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "delivery": "queued", "session_id": id, "question": q.Public(), "clientRequestId": q.SubmissionID})
		return
	}
	if err := s.flushContextMessages(id, 0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	st, ctx, err := s.occupy(context.WithoutCancel(r.Context()), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	enableRunInbox(st)
	st.inputMetadata = message
	go s.runPrompt(ctx, st, id, message.Content, nil, "", "", "", s.takeNextTurn(id))
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "delivery": "started", "session_id": id, "question": q.Public(), "clientRequestId": q.SubmissionID})
}

func (s *Server) pushQuestionAnswer(st *runState, message types.Message) bool {
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.steerClosed || st.inbox == nil || st.cancelReason != "" {
		return false
	}
	if st.questionSubmissions == nil {
		st.questionSubmissions = make(map[string]bool)
	}
	if st.questionSubmissions[message.ClientRequestID] || st.inputMetadata.ClientRequestID == message.ClientRequestID {
		return true
	}
	st.questionSubmissions[message.ClientRequestID] = true
	st.inbox.Push(message)
	return true
}

func (s *Server) restoreQuestionAnswers(sess *session.Session, st *runState) {
	for _, q := range sess.Questions() {
		if q.Status == "answer_queued" && q.SourceBranchValid {
			s.pushQuestionAnswer(st, sess.QuestionAnswerMessage(q))
		}
	}
}

func (s *Server) questionsProjection(id string) []session.Question {
	dir, err := s.sessionDir(id)
	if err != nil {
		return []session.Question{}
	}
	questions, err := session.QuestionsAt(dir)
	if err != nil {
		return []session.Question{}
	}
	for i := range questions {
		questions[i] = questions[i].Public()
	}
	return questions
}

func isQuestionTool(name string) bool {
	return name == catalog.RequestUserInputAsync
}
