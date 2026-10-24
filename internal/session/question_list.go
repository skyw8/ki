package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"ki/internal/state"
	"ki/internal/types"
)

const questionListVersion = 1

type questionListSummary struct {
	Version  int         `json:"version"`
	Size     int64       `json:"size"`
	Modified time.Time   `json:"modified"`
	Offset   int64       `json:"offset"`
	Anchor   string      `json:"anchor"`
	Nodes    [][2]string `json:"nodes,omitempty"`
	Facts    []Entry     `json:"facts,omitempty"`
	Leaf     string      `json:"leaf,omitempty"`
}

func (s *questionListSummary) matches(stamp fileStamp) bool {
	return s.Size == stamp.size && s.Modified.Equal(stamp.mtime)
}

func questionListPath(dir string) string { return filepath.Join(dir, "question-list.json") }

func readQuestionListSummary(dir string) *questionListSummary {
	raw, _, err := state.ReadFile(questionListPath(dir), questionListVersion, nil)
	if err != nil {
		return nil
	}
	var summary questionListSummary
	if json.Unmarshal(raw, &summary) != nil || summary.Offset < 0 || summary.Offset > summary.Size {
		return nil
	}
	return &summary
}

func (s *questionListSummary) links(leaf string) []QuestionLink {
	if len(s.Facts) == 0 {
		return nil
	}
	if leaf == "" {
		leaf = s.Leaf
	}
	entries := make([]Entry, 0, len(s.Nodes)+len(s.Facts))
	for _, node := range s.Nodes {
		entries = append(entries, Entry{ID: node[0], ParentID: node[1]})
	}
	entries = append(entries, s.Facts...)
	var links []QuestionLink
	for _, q := range ProjectQuestions(entries, leaf) {
		if q.SourceBranchValid || q.SideSessionID != "" {
			links = append(links, QuestionLink{QuestionItemID: q.QuestionItemID, Title: q.Title, Status: q.Status, Revision: q.Revision, SideSessionID: q.SideSessionID})
		}
	}
	return links
}

func buildQuestionListSummary(dir string, prior *questionListSummary, stop <-chan struct{}) (*questionListSummary, error) {
	gate := fileGate(dir)
	gate.RLock()
	defer gate.RUnlock()
	f, err := os.Open(entriesPath(dir))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	next := &questionListSummary{Version: questionListVersion, Size: stat.Size(), Modified: stat.ModTime()}
	if prior != nil && prior.Size <= stat.Size() && !stat.ModTime().Before(prior.Modified) && (prior.Size < stat.Size() || prior.Modified.Equal(stat.ModTime())) {
		anchor, err := questionListAnchor(f, prior.Offset)
		if err == nil && anchor == prior.Anchor {
			*next = *prior
			next.Nodes, next.Facts = slices.Clone(prior.Nodes), slices.Clone(prior.Facts)
			next.Size, next.Modified = stat.Size(), stat.ModTime()
		}
	}
	if _, err := f.Seek(next.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(io.LimitReader(f, next.Size-next.Offset), 256<<10)
	for {
		select {
		case <-stop:
			return nil, errors.New("question summary closed")
		default:
		}
		line, err := readTranscriptLine(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			next.Offset += int64(len(line))
			continue
		}
		if err := next.consume(line); err != nil {
			return nil, err
		}
		next.Offset += int64(len(line))
	}
	next.Anchor, err = questionListAnchor(f, next.Offset)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(entriesPath(dir)); err != nil {
		return nil, err
	}
	_, _, readErr := state.ReadFile(questionListPath(dir), questionListVersion, nil)
	if !errors.Is(readErr, state.ErrNewerVersion) {
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			_ = state.WriteJSON(questionListPath(dir), next, 0o600)
		} else {
			_ = state.WriteVersioned(questionListPath(dir), questionListVersion, next, 0o600)
		}
	}
	return next, nil
}

func questionListAnchor(f *os.File, end int64) (string, error) {
	start := max(int64(0), end-4096)
	buf := make([]byte, end-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

func questionListHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (s *questionListSummary) consume(raw []byte) error {
	var wire struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		ParentID  string          `json:"parentId"`
		Timestamp string          `json:"timestamp"`
		Sideband  bool            `json:"sideband"`
		Details   json.RawMessage `json:"details"`
		Message   *struct {
			Role            string               `json:"role"`
			ClientRequestID string               `json:"clientRequestId"`
			QuestionReply   *types.QuestionReply `json:"questionReply"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if wire.Type == "session" || wire.ID == "" {
		return nil
	}
	e := Entry{ID: wire.ID, ParentID: wire.ParentID, Type: wire.Type, Sideband: wire.Sideband, Timestamp: wire.Timestamp}
	if !wire.Sideband {
		s.Nodes = append(s.Nodes, [2]string{wire.ID, wire.ParentID})
		s.Leaf = wire.ID
	}
	var details struct {
		Questions []Question `json:"questions"`
		Question  *Question  `json:"question"`
	}
	if wire.Type == "question_requested" || wire.Type == "question_updated" {
		if err := json.Unmarshal(wire.Details, &details); err != nil {
			return err
		}
	}
	minimal := func(q Question) Question {
		return Question{QuestionItemID: q.QuestionItemID, Title: q.Title, SourceEntryID: q.SourceEntryID, Status: q.Status, Revision: q.Revision, SideSessionID: q.SideSessionID, SubmissionID: q.SubmissionID, AnswerEntryID: q.AnswerEntryID, Answer: questionListHash(q.Answer)}
	}
	switch wire.Type {
	case "question_requested":
		questions := make([]Question, len(details.Questions))
		for i, q := range details.Questions {
			questions[i] = minimal(q)
		}
		e.Details = map[string]any{"questions": questions}
		s.Facts = append(s.Facts, e)
	case "question_updated":
		if details.Question != nil {
			e.Details = map[string]any{"question": minimal(*details.Question)}
			s.Facts = append(s.Facts, e)
		}
	case "message":
		if m := wire.Message; m != nil && m.Role == "user" && m.QuestionReply != nil {
			reply := *m.QuestionReply
			reply.Answer = questionListHash(reply.Answer)
			e.Message = &types.Message{Role: m.Role, ClientRequestID: m.ClientRequestID, QuestionReply: &reply}
			s.Facts = append(s.Facts, e)
		}
	}
	return nil
}
