package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"ki/internal/loop"
	"ki/internal/types"
)

func TestCompactSnapshotReplayKeepsConcurrentToolAndNewMessages(t *testing.T) {
	em, sess, hs := newEmitterForTest(t)
	em.s.mu.Lock()
	em.s.runs[sess.ID()] = em.st
	em.s.mu.Unlock()
	emit := func(ev loop.Event) {
		t.Helper()
		if err := em.Emit(ev); err != nil {
			t.Fatal(err)
		}
	}
	message := func(role, body, call string) types.Message {
		return types.Message{Role: role, ToolCallID: call, Content: []types.Content{{Type: "text", Text: body}}}
	}
	user := message("user", "input", "")
	hidden := message("assistant", "HIDDEN_ASSISTANT", "")
	result := message("toolResult", "HIDDEN_RESULT", "finished")
	emit(loop.Event{Type: loop.AgentStart})
	emit(loop.Event{Type: loop.MessageEnd, Message: &user})
	emit(loop.Event{Type: loop.RequestHeader, System: "HIDDEN_SYSTEM"})
	emit(loop.Event{Type: loop.MessageEnd, Message: &hidden})
	emit(loop.Event{Type: loop.ToolExecutionStart, ToolCallID: "finished", Args: map[string]any{"command": "HIDDEN_ARGS"}})
	emit(loop.Event{Type: loop.ToolExecutionStart, ToolCallID: "running", Args: map[string]any{"command": "still needed"}})
	emit(loop.Event{Type: loop.ToolExecutionEnd, ToolCallID: "finished", Result: "HIDDEN_RESULT"})
	emit(loop.Event{Type: loop.MessageEnd, Message: &result})
	leaf := em.st.evs[len(em.st.evs)-1].EntryID
	emit(loop.Event{Type: loop.TurnEnd, Message: &hidden, ToolResults: []types.Message{result}})
	emit(loop.Event{Type: loop.ToolExecutionUpdate, ToolCallID: "running", PartialResult: "new progress"})
	newResult := message("toolResult", "new result", "running")
	emit(loop.Event{Type: loop.MessageEnd, Message: &newResult})
	newReply := message("assistant", "new reply", "")
	emit(loop.Event{Type: loop.MessageEnd, Message: &newReply})
	emit(loop.Event{Type: loop.AgentEnd, Messages: []types.Message{hidden, result, newReply}})
	close(em.st.done)
	for _, through := range []string{leaf, "invalid", ""} {
		t.Run(through, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/events?through=%s", hs.URL, sess.ID(), through), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer tok")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status %d", res.StatusCode)
			}
			scanner := bufio.NewScanner(res.Body)
			var data string
			var messages []string
			var running, ended bool
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data += line
				var ev loop.Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
					t.Fatal(err)
				}
				if ev.Type == loop.MessageEnd {
					messages = append(messages, ev.Message.Text())
				}
				if ev.Type == loop.ToolExecutionStart && ev.ToolCallID == "running" {
					running = ev.Args["command"] == "still needed"
				}
				if ev.Type == loop.AgentEnd {
					ended = true
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if !running || !ended {
				t.Fatalf("lost running tool or lifecycle: %s", data)
			}
			if through == leaf {
				if strings.Contains(data, "HIDDEN_") {
					t.Fatalf("snapshot bodies replayed: %s", data)
				}
				if strings.Join(messages, ",") != "new result,new reply" {
					t.Fatalf("lost messages after snapshot: %v", messages)
				}
			} else if !strings.Contains(data, "HIDDEN_ASSISTANT") {
				t.Fatal("ordinary/invalid snapshot must keep full replay")
			}
		})
	}
}
