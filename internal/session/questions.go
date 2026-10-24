package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"ki/internal/types"
)

type QuestionInput struct {
	Title   string   `json:"title"`
	Options []string `json:"options,omitempty"`
}

type QuestionAnswerForm struct {
	Selected string `json:"selected"`
	Text     string `json:"text"`
}

type Question struct {
	QuestionItemID         string              `json:"questionItemId"`
	CallID                 string              `json:"callId"`
	Index                  int                 `json:"index"`
	Title                  string              `json:"title"`
	Options                []string            `json:"options,omitempty"`
	Status                 string              `json:"status"`
	Revision               int64               `json:"revision"`
	CreatedAt              string              `json:"createdAt"`
	UpdatedAt              string              `json:"updatedAt"`
	SourceEntryID          string              `json:"sourceEntryId"`
	SnapshotEntryID        string              `json:"snapshotEntryId"`
	SourceBranchValid      bool                `json:"sourceBranchValid"`
	SideSessionID          string              `json:"sideSessionId,omitempty"`
	Answer                 string              `json:"answer,omitempty"`
	AnswerForm             *QuestionAnswerForm `json:"answerForm,omitempty"`
	SubmissionID           string              `json:"submissionId,omitempty"`
	AnswerEntryID          string              `json:"answerEntryId,omitempty"`
	SnapshotProvider       string              `json:"snapshotProvider,omitempty"`
	SnapshotModel          string              `json:"snapshotModel,omitempty"`
	SnapshotThinkingEffort string              `json:"snapshotThinkingEffort,omitempty"`
	SupplementalUser       *types.Message      `json:"supplementalUser,omitempty"`
	SnapshotResources      json.RawMessage     `json:"snapshotResources,omitempty"`
	SnapshotCWD            string              `json:"snapshotCwd,omitempty"`
	AnswerBranchValid      bool                `json:"answerBranchValid"`
}

var ErrQuestionConflict = errors.New("question revision conflict")

func (q Question) Public() Question {
	q.SupplementalUser = nil
	q.SnapshotProvider = ""
	q.SnapshotModel = ""
	q.SnapshotThinkingEffort = ""
	q.SnapshotResources = nil
	q.SnapshotCWD = ""
	return q
}

var questionGates sync.Map

func questionGate(dir string) *sync.Mutex {
	mu, _ := questionGates.LoadOrStore(dir, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func questionDetails(details any, key string, out any) bool {
	b, err := json.Marshal(details)
	if err != nil {
		return false
	}
	if key != "" {
		var object map[string]json.RawMessage
		if json.Unmarshal(b, &object) != nil {
			return false
		}
		b = object[key]
	}
	return len(b) > 0 && json.Unmarshal(b, out) == nil
}

func PublicQuestionEntry(entry Entry) (Entry, error) {
	var details map[string]json.RawMessage
	if !questionDetails(entry.Details, "", &details) {
		return Entry{}, errors.New("invalid question event details")
	}
	delete(details, "snapshot")
	if raw := details["questions"]; raw != nil {
		var questions []Question
		if err := json.Unmarshal(raw, &questions); err != nil {
			return Entry{}, err
		}
		for i := range questions {
			questions[i] = questions[i].Public()
		}
		details["questions"], _ = json.Marshal(questions)
	}
	if raw := details["question"]; raw != nil {
		var question Question
		if err := json.Unmarshal(raw, &question); err != nil {
			return Entry{}, err
		}
		details["question"], _ = json.Marshal(question.Public())
	}
	entry.Details = details
	return entry, nil
}

func ProjectQuestions(entries []Entry, leaf string) []Question {
	if !slices.ContainsFunc(entries, func(entry Entry) bool { return entry.Type == "question_requested" }) {
		return []Question{}
	}
	byID := make(map[string]Question)
	var order []string
	branch := make(map[string]bool)
	entryByID := make(map[string]Entry, len(entries))
	for _, e := range entries {
		entryByID[e.ID] = e
	}
	for at := leaf; at != ""; {
		if branch[at] {
			break
		}
		branch[at] = true
		e, ok := entryByID[at]
		if !ok {
			break
		}
		at = e.ParentID
	}
	children := make(map[string][]string)
	for _, e := range entries {
		children[e.ParentID] = append(children[e.ParentID], e.ID)
	}
	type visit struct {
		id   string
		exit bool
	}
	tin, tout := make(map[string]int), make(map[string]int)
	stack := []visit{{id: ""}}
	clock := 0
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if v.exit {
			tout[v.id] = clock
			continue
		}
		if _, seen := tin[v.id]; seen {
			continue
		}
		clock++
		tin[v.id] = clock
		stack = append(stack, visit{id: v.id, exit: true})
		for _, child := range children[v.id] {
			stack = append(stack, visit{id: child})
		}
	}
	for _, e := range entries {
		var qs []Question
		switch e.Type {
		case "question_requested":
			questionDetails(e.Details, "questions", &qs)
			var snapshot Question
			if questionDetails(e.Details, "snapshot", &snapshot) {
				for i := range qs {
					qs[i] = inheritQuestionSnapshot(qs[i], snapshot)
				}
			}
		case "question_updated":
			var q Question
			if questionDetails(e.Details, "question", &q) {
				qs = []Question{q}
			}
		}
		for _, q := range qs {
			if q.QuestionItemID == "" {
				continue
			}
			if prior, ok := byID[q.QuestionItemID]; !ok {
				order = append(order, q.QuestionItemID)
			} else if prior.Revision >= q.Revision {
				continue
			} else {
				q = inheritQuestionSnapshot(q, prior)
			}
			byID[q.QuestionItemID] = q
		}
	}
	for _, e := range entries {
		if e.Message == nil || e.Message.Role != "user" || e.Message.QuestionReply == nil {
			continue
		}
		q, ok := byID[e.Message.QuestionReply.QuestionItemID]
		if !ok || q.SubmissionID == "" || e.Message.ClientRequestID != q.SubmissionID ||
			e.Message.QuestionReply.Title != q.Title || e.Message.QuestionReply.Answer != q.Answer ||
			(q.AnswerEntryID != "" && q.AnswerEntryID != e.ID) {
			continue
		}
		sourceOnBranch := q.SourceEntryID == "" || (tin[q.SourceEntryID] > 0 &&
			tin[q.SourceEntryID] <= tin[e.ID] && tout[q.SourceEntryID] >= tout[e.ID])
		if !sourceOnBranch {
			continue
		}
		if q.Status != "answered" {
			q.Revision++
		}
		q.Status, q.AnswerEntryID, q.UpdatedAt = "answered", e.ID, e.Timestamp
		q.AnswerBranchValid = branch[e.ID]
		byID[q.QuestionItemID] = q
	}
	out := make([]Question, 0, len(order))
	for _, id := range order {
		q := byID[id]
		q.SourceBranchValid = q.SourceEntryID == "" || branch[q.SourceEntryID]
		out = append(out, q)
	}
	return out
}

func (s *Session) Questions() []Question {
	questions, _ := questionsAt(s.Dir, true)
	return questions
}

type QuestionLink struct {
	QuestionItemID string `json:"questionItemId"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Revision       int64  `json:"revision"`
	SideSessionID  string `json:"sideSessionId"`
}

func QuestionsAt(dir string) ([]Question, error) {
	return questionsAt(dir, false)
}

func questionsAt(dir string, includeSnapshot bool) ([]Question, error) {
	cfg, err := ReadConfig(dir)
	if err != nil {
		return nil, err
	}
	if cfg.Metadata["purpose"] == "question_side" {
		return []Question{}, nil
	}
	transcript, err := ReadTranscript(dir)
	if err != nil {
		return nil, err
	}
	leaf := cfg.ActiveLeafID
	if leaf == "" {
		for _, e := range transcript.Entries() {
			if !e.Sideband {
				leaf = e.ID
			}
		}
	}
	entries := transcript.Entries()
	if includeSnapshot {
		var ids []string
		for _, entry := range entries {
			if entry.Type == "question_requested" {
				ids = append(ids, entry.ID)
			}
		}
		if len(ids) > 0 {
			facts, err := transcript.Lookup(ids)
			if err != nil {
				return nil, err
			}
			byID := make(map[string]Entry, len(facts))
			for _, fact := range facts {
				byID[fact.ID] = fact
			}
			entries = slices.Clone(entries)
			for i := range entries {
				if fact, ok := byID[entries[i].ID]; ok {
					entries[i] = fact
				}
			}
		}
	}
	return ProjectQuestions(entries, leaf), nil
}

func (s *Session) Question(id string) (Question, bool) {
	for _, q := range s.Questions() {
		if q.QuestionItemID == id {
			return q, true
		}
	}
	return Question{}, false
}

func (s *Session) RequestQuestions(callID string, inputs []QuestionInput, resources ...json.RawMessage) ([]Question, error) {
	gate := questionGate(s.Dir)
	gate.Lock()
	defer gate.Unlock()
	all := s.Questions()
	var existing []Question
	for _, q := range all {
		if q.CallID == callID {
			existing = append(existing, q)
		}
	}
	s.mu.Lock()
	path := s.leafEntriesLocked(s.leafID)
	source := ""
	for _, e := range path {
		if e.Message == nil || e.Message.Role != "assistant" {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "toolCall" && c.ID == callID {
				source = e.ID
			}
		}
	}
	if source == "" {
		s.mu.Unlock()
		return nil, errors.New("question source tool call not found on active branch")
	}
	if len(existing) > 0 {
		s.mu.Unlock()
		if len(existing) != len(inputs) {
			return nil, ErrQuestionConflict
		}
		for i, q := range existing {
			if q.SourceEntryID != source || q.Title != inputs[i].Title || !slices.Equal(q.Options, inputs[i].Options) {
				return nil, ErrQuestionConflict
			}
		}
		return existing, nil
	}
	snapshot := ""
	pending := make(map[string]bool)
	for _, e := range path {
		if e.ID == source {
			break
		}
		if e.Type == "compaction" {
			if !protocolComplete(s.messagesLocked(e.ID)) {
				break
			}
			clear(pending)
		} else if e.Message != nil && !advanceQuestionProtocol(*e.Message, pending) {
			break
		}
		if len(pending) == 0 {
			snapshot = e.ID
		}
	}
	var supplemental *types.Message
	snapshotPath := s.leafEntriesLocked(snapshot)
	inSnapshot := make(map[string]bool, len(snapshotPath))
	for _, e := range snapshotPath {
		inSnapshot[e.ID] = true
	}
	for i := len(path) - 1; i >= 0; i-- {
		e := path[i]
		if e.Message != nil && e.Message.Role == "user" && !e.Message.ContextOnly {
			if !inSnapshot[e.ID] {
				msg := *e.Message
				supplemental = &msg
			}
			break
		}
	}
	provider, model, effort := s.Config.Provider, s.Config.Model, s.Config.ThinkingEffort
	cwd := s.Header.CWD
	s.mu.Unlock()
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve question workspace: %w", err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	questions := make([]Question, 0, len(inputs))
	var frozenResources json.RawMessage
	if len(resources) > 0 {
		frozenResources = slices.Clone(resources[0])
	}
	for i, input := range inputs {
		rawID, _ := json.Marshal([]any{"request_user_input_async", callID, i})
		q := Question{
			QuestionItemID: string(rawID), CallID: callID, Index: i, Title: input.Title, Options: slices.Clone(input.Options),
			Status: "pending", Revision: 1, CreatedAt: stamp, UpdatedAt: stamp, SourceEntryID: source,
			SnapshotEntryID: snapshot, SourceBranchValid: true, SnapshotProvider: provider, SnapshotModel: model,
			SnapshotThinkingEffort: effort, SupplementalUser: supplemental,
			SnapshotCWD: cwd,
		}
		q.SnapshotResources = frozenResources
		questions = append(questions, q)
	}
	persisted := make([]Question, len(questions))
	for i, q := range questions {
		persisted[i] = q.Public()
	}
	details := map[string]any{"questions": persisted}
	if len(questions) > 0 {
		q := questions[0]
		details["snapshot"] = map[string]any{
			"snapshotEntryId": q.SnapshotEntryID, "snapshotProvider": q.SnapshotProvider, "snapshotModel": q.SnapshotModel,
			"snapshotThinkingEffort": q.SnapshotThinkingEffort, "snapshotCwd": q.SnapshotCWD,
			"snapshotResources": q.SnapshotResources, "supplementalUser": q.SupplementalUser,
		}
	}
	if _, err := AppendSidebandEvent(s.Dir, "question_requested", details); err != nil {
		return nil, err
	}
	return questions, nil
}

func (s *Session) UpdateQuestion(q Question) error {
	gate := questionGate(s.Dir)
	gate.Lock()
	defer gate.Unlock()
	prior, ok := s.Question(q.QuestionItemID)
	if !ok || q.Revision != prior.Revision+1 {
		return ErrQuestionConflict
	}
	if q.CallID != prior.CallID || q.SourceEntryID != prior.SourceEntryID || q.Title != prior.Title {
		return fmt.Errorf("immutable question source")
	}
	if q.UpdatedAt == "" || q.UpdatedAt == prior.UpdatedAt {
		q.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := AppendSidebandEvent(s.Dir, "question_updated", map[string]any{"question": q.Public()})
	return err
}

func inheritQuestionSnapshot(q, snapshot Question) Question {
	q.SnapshotEntryID = snapshot.SnapshotEntryID
	q.SnapshotCWD = snapshot.SnapshotCWD
	q.SnapshotProvider = snapshot.SnapshotProvider
	q.SnapshotModel = snapshot.SnapshotModel
	q.SnapshotThinkingEffort = snapshot.SnapshotThinkingEffort
	q.SnapshotResources = snapshot.SnapshotResources
	q.SupplementalUser = snapshot.SupplementalUser
	return q
}

func protocolComplete(messages []types.Message) bool {
	pending := make(map[string]bool)
	for _, m := range messages {
		if !advanceQuestionProtocol(m, pending) {
			return false
		}
	}
	return len(pending) == 0
}

func advanceQuestionProtocol(m types.Message, pending map[string]bool) bool {
	if m.Role == "toolResult" {
		if !pending[m.ToolCallID] {
			return false
		}
		delete(pending, m.ToolCallID)
		return true
	}
	if len(pending) > 0 {
		return false
	}
	if m.Role == "assistant" {
		for _, c := range m.Content {
			if c.Type == "toolCall" {
				if c.ID == "" || pending[c.ID] {
					return false
				}
				pending[c.ID] = true
			}
		}
	}
	return true
}

func (s *Session) QuestionAnswerMessage(q Question) types.Message {
	payload, _ := json.Marshal([]map[string]string{{
		"questionItemId": q.QuestionItemID, "question": q.Title, "answer": q.Answer,
	}})
	return types.Message{
		Role: "user", ClientRequestID: q.SubmissionID,
		QuestionReply: &types.QuestionReply{QuestionItemID: q.QuestionItemID, Title: q.Title, Answer: q.Answer},
		Content:       []types.Content{{Type: "text", Text: "<send_user_message_question_reply>\n" + string(payload) + "\n</send_user_message_question_reply>"}},
	}
}
