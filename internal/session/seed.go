package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ki/internal/idgen"
	"ki/internal/types"
)

// SeedSpec builds a synthetic transcript for view/HTTP/WebUI performance tests.
type SeedSpec struct {
	Turns            int
	AssistantBytes   int
	ToolResultBytes  int
	SystemBytes      int
	Title            string
	RepeatSamePrompt bool
}

// SeedTranscript appends Turns of user / request_header / assistant (/ toolResult)
// in one write. Why: production appends fsync every row; that would dominate
// fixture setup and hide Open/BuildView cost.
func (s *Session) SeedTranscript(spec SeedSpec) error {
	if spec.Turns <= 0 {
		return fmt.Errorf("turns required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sys := spec.SystemBytes
	if sys <= 0 {
		sys = 256
	}
	system := "perf-system " + strings.Repeat("S", sys)
	tools := []ToolSchema{{Name: "Read", Description: "Read a file", Parameters: map[string]any{"type": "object"}}}
	asst := spec.AssistantBytes
	if asst <= 0 {
		asst = 32
	}
	assistantBody := strings.Repeat("A", asst)
	var toolBody string
	if spec.ToolResultBytes > 0 {
		toolBody = strings.Repeat("T", spec.ToolResultBytes)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	var buf bytes.Buffer
	parent := s.leafID
	for i := 0; i < spec.Turns; i++ {
		user, err := seedMessage(parent, stamp, types.Message{
			Role:    "user",
			Content: []types.Content{{Type: "text", Text: "turn " + strconv.Itoa(i)}},
		})
		if err != nil {
			return err
		}
		parent = user.ID
		s.track(user)
		if err := writeJSONLine(&buf, user); err != nil {
			return err
		}

		headerID, err := idgen.EntryID()
		if err != nil {
			return err
		}
		headerSys := system
		if !spec.RepeatSamePrompt {
			// Why: the view only omits system/tools when the prompt is unchanged.
			// Varying the body lets tests contrast that path against RepeatSamePrompt.
			headerSys = system + " " + strconv.Itoa(i)
		}
		header := Entry{
			Type:      "request_header",
			ID:        headerID,
			ParentID:  parent,
			Timestamp: stamp,
			System:    headerSys,
			Tools:     tools,
			Provider:  s.Config.Provider,
			ModelID:   s.Config.Model,
		}
		parent = header.ID
		s.track(header)
		if err := writeJSONLine(&buf, header); err != nil {
			return err
		}

		callID := "call" + strconv.Itoa(i)
		content := []types.Content{{Type: "text", Text: assistantBody}}
		if toolBody != "" {
			content = append(content, types.Content{Type: "toolCall", ID: callID, Name: "Read", Arguments: map[string]any{"file_path": "f.txt"}})
		}
		asstEntry, err := seedMessage(parent, stamp, types.Message{
			Role:    "assistant",
			Content: content,
			Usage:   &types.Usage{Input: 10, Output: 4},
		})
		if err != nil {
			return err
		}
		parent = asstEntry.ID
		s.track(asstEntry)
		if err := writeJSONLine(&buf, asstEntry); err != nil {
			return err
		}

		if toolBody == "" {
			continue
		}
		tool, err := seedMessage(parent, stamp, types.Message{
			Role:       "toolResult",
			ToolCallID: callID,
			ToolName:   "Read",
			Content:    []types.Content{{Type: "text", Text: toolBody}},
		})
		if err != nil {
			return err
		}
		parent = tool.ID
		s.track(tool)
		if err := writeJSONLine(&buf, tool); err != nil {
			return err
		}
	}
	s.leafID = parent
	s.Config.ActiveLeafID = parent
	if spec.Title != "" {
		s.Config.Title = spec.Title
	}

	gate := fileGate(s.Dir)
	gate.Lock()
	_, err := s.jsonl.Write(buf.Bytes())
	if err == nil {
		err = s.jsonl.Sync()
	}
	gate.Unlock()
	if err != nil {
		return err
	}
	return s.writeConfig()
}

func (s *Session) track(e Entry) {
	s.entries = append(s.entries, e)
	s.byID[e.ID] = e
}

func seedMessage(parent, stamp string, m types.Message) (Entry, error) {
	id, err := idgen.EntryID()
	if err != nil {
		return Entry{}, err
	}
	return Entry{Type: "message", ID: id, ParentID: parent, Timestamp: stamp, Message: &m}, nil
}

func writeJSONLine(buf *bytes.Buffer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = buf.Write(append(b, '\n'))
	return err
}
