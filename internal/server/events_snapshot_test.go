package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"ki/internal/loop"
	"ki/internal/types"
)

func TestSnapshotReplayUserIdentityBeforeEndBuffered(t *testing.T) {
	em, sess, _ := newEmitterForTest(t)
	hidden := types.Message{Role: "user", Origin: "agent:/root/child", ClientRequestID: "persisted",
		Content: []types.Content{{Type: "text", Text: "same text"}}}
	end := loop.Event{Type: loop.MessageEnd, Message: &hidden}
	// Reproduce the exact persist-before-buffer window without a timing race.
	if err := em.persist(&end); err != nil {
		t.Fatal(err)
	}
	snapshot := em.s.replaySnapshot(sess.ID(), end.EntryID)
	if snapshot.through != 0 || !snapshot.users[hidden.ClientRequestID] {
		t.Fatalf("snapshot lost persisted identity: %+v", snapshot)
	}
	later := hidden
	later.ClientRequestID = "not-on-snapshot-branch"
	laterEnd := loop.Event{Type: loop.MessageEnd, Message: &later}
	if err := em.persist(&laterEnd); err != nil {
		t.Fatal(err)
	}
	// A later persisted entry must not count as part of an older snapshot.
	snapshot = em.s.replaySnapshot(sess.ID(), end.EntryID)
	for _, kind := range []loop.EventType{loop.SteerAccepted, loop.MessageStart} {
		if !snapshot.covers(&loop.Event{Type: kind, Message: &hidden, Seq: 10}) {
			t.Fatalf("%s escaped snapshot before end was buffered", kind)
		}
		if snapshot.covers(&loop.Event{Type: kind, Message: &later, Seq: 1}) {
			t.Fatalf("%s covered an identity outside the snapshot", kind)
		}
	}
	// Acceptance can be older than the last completed message, but that does
	// not establish persistence: the accepted message may still be in Inbox.
	snapshot.observe(&loop.Event{Type: loop.MessageEnd, EntryID: end.EntryID, Seq: 20})
	if snapshot.covers(&loop.Event{Type: loop.SteerAccepted, Message: &later, Seq: 1}) {
		t.Fatal("sequence cutoff swallowed undrained acceptance")
	}
	if snapshot.covers(&loop.Event{Type: loop.MessageStart, Message: &later, Seq: 1}) {
		t.Fatal("sequence cutoff swallowed a user start outside the snapshot")
	}
	unidentified := hidden
	unidentified.ClientRequestID = ""
	if snapshot.covers(&loop.Event{Type: loop.SteerAccepted, Message: &unidentified, Seq: 1}) {
		t.Fatal("equal text established acceptance identity")
	}
	if !snapshot.covers(&end) {
		t.Fatal("persisted end escaped snapshot")
	}
}

func TestSteerAcceptanceOnlyPublishesHumanInputs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request steerRequest
		visible bool
	}{
		{name: "human", visible: true},
		{name: "extension relay", request: steerRequest{Origin: "extension:telegram"}, visible: true},
		{name: "runtime directive", request: steerRequest{Origin: "agent:/root/child"}},
		{name: "runtime mailbox", request: steerRequest{Origin: "agent:/root/child", ContextOnly: true}},
		{name: "context-only", request: steerRequest{ContextOnly: true}},
		{name: "completion", request: steerRequest{Completion: &types.CompletionIdentity{TaskID: "child", Generation: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &runState{inbox: &loop.Inbox{}, partial: -1}
			st.wait = sync.NewCond(&st.mu)
			if !(&Server{}).pushSteerRun(st, tc.request) {
				t.Fatal("input was rejected")
			}
			pending := st.inbox.Take()
			if len(pending) != 1 || pending[0].ClientRequestID == "" ||
				pending[0].Origin != tc.request.Origin || pending[0].ContextOnly != tc.request.ContextOnly {
				t.Fatalf("input was not retained: %+v", pending)
			}
			if got := len(st.evs) != 0; got != tc.visible {
				t.Fatalf("optimistic acceptance = %v, want %v", got, tc.visible)
			}
			if tc.visible && (len(st.evs) != 1 || st.evs[0].Type != loop.SteerAccepted ||
				st.evs[0].Message.ClientRequestID != pending[0].ClientRequestID) {
				t.Fatalf("acceptance lost correlation: %+v", st.evs)
			}
		})
	}
}

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
	emit(loop.Event{Type: loop.ToolExecutionStart, ToolCallID: "finished", Args: map[string]any{"cmd": "HIDDEN_ARGS"}})
	emit(loop.Event{Type: loop.ToolExecutionStart, ToolCallID: "running", Args: map[string]any{"cmd": "still needed"}})
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
					running = ev.Args["cmd"] == "still needed"
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
