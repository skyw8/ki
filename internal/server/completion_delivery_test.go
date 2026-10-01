package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/agent"
	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/types"
)

// A gated child lets tests exercise durable completion and mailbox handoff without sleeps.
type completionRaceStreamer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *completionRaceStreamer) Stream(ctx context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	for _, m := range req.Messages {
		if strings.Contains(m.Text(), "a child agent of") {
			s.once.Do(func() { close(s.started) })
			select {
			case <-s.release:
				return types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "child result"}}, StopReason: "stop"}, nil
			case <-ctx.Done():
				return types.Message{}, ctx.Err()
			}
		}
	}
	return types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: "parent result"}}, StopReason: "stop"}, nil
}
func completionCount(t *testing.T, srv *Server, id string) int {
	t.Helper()
	sess, err := srv.open(id)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	count := 0
	for _, m := range sess.MessagesToLeaf() {
		if m.Completion != nil {
			count++
			if m.ClientRequestID == "" || m.Origin == "" {
				t.Fatalf("lost completion identity: %+v", m)
			}
		}
	}
	return count
}
func TestCompletionDeliveryMailboxHandoff(t *testing.T) {
	for _, mode := range []string{"live", "idle", "handoff", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			stream := &completionRaceStreamer{started: make(chan struct{}), release: make(chan struct{})}
			srv, hs := testServerWith(t, stream)
			root := createSession(t, hs, t.TempDir())
			st, runCtx, err := srv.occupy(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			enableRunInbox(st)
			launch, err := srv.SpawnAgent(t.Context(), agent.Request{TaskName: "child", ParentSessionID: root, Prompt: "child task", ForkTurns: "none"})
			if err != nil {
				t.Fatal(err)
			}
			<-stream.started
			if mode == "timeout" {
				result, err := srv.WaitAgent(t.Context(), root, time.Millisecond)
				if err != nil || !result.TimedOut {
					t.Fatalf("wait: %+v %v", result, err)
				}
			}
			if mode == "cancel" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if _, err := srv.WaitAgent(ctx, root, time.Minute); err == nil {
					t.Fatal("cancel ignored")
				}
				if snapshot, _ := srv.Get(launch.TaskID); snapshot.Status != agent.Running {
					t.Fatal("observer canceled child")
				}
			}
			if mode == "idle" {
				srv.release(root, st)
			}
			close(stream.release)
			if _, err := srv.Wait(t.Context(), launch.TaskID); err != nil {
				t.Fatal(err)
			}
			// Inspection cannot consume a completion; repeat reads remain observational.
			for range 2 {
				views, err := srv.ListAgents(root, "")
				if err != nil || len(views) != 2 {
					t.Fatalf("views: %+v %v", views, err)
				}
			}
			if mode == "live" {
				result, err := srv.WaitAgent(t.Context(), root, time.Second)
				if err != nil || result.TimedOut {
					t.Fatalf("mailbox missed completion: %+v %v", result, err)
				}
			}
			if mode == "handoff" {
				srv.preserveRunInbox(root, st)
				srv.release(root, st)
			}
			if mode == "idle" || mode == "handoff" {
				if srv.running(root) {
					t.Fatal("completion started a parent turn")
				}
				dir, _ := srv.sidx.Lookup(root)
				pending, err := session.ReadContextQueue(dir)
				if err != nil || (len(pending) != 1 && completionCount(t, srv, root) != 1) {
					t.Fatalf("durable handoff: %+v %v", pending, err)
				}
				st, runCtx, err = srv.occupy(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
			}
			srv.runPrompt(runCtx, st, root, []types.Content{{Type: "text", Text: "parent task"}}, nil, "", "", "", nil)
			if completionCount(t, srv, root) != 1 {
				t.Fatal("completion not persisted exactly once")
			}
			snap, _ := srv.Get(launch.TaskID)
			identity := types.CompletionIdentity{TaskID: launch.TaskID, Generation: snap.Generation}
			srv.notifyAgentCompletion(root, identity, "child", "", agent.Completion{Result: "duplicate"}, nil)
			if completionCount(t, srv, root) != 1 || srv.running(root) {
				t.Fatal("duplicate completion changed work state")
			}
		})
	}
}

func TestPromptIdentityQueuePromotion(t *testing.T) {
	stream := newHoldTurnStreamer()
	srv, hs := testServerWith(t, stream)
	t.Cleanup(stream.unblock)
	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "start")
	<-stream.entered
	status, accepted := promptJSON(t, hs, id, "same text", map[string]any{"delivery": "queue", "clientRequestId": "original-request"})
	if status != 202 || accepted["clientRequestId"] != "original-request" {
		t.Fatalf("accepted: %d %+v", status, accepted)
	}
	dir, _ := srv.sidx.Lookup(id)
	queue, err := session.ReadQueue(dir)
	if err != nil || len(queue) != 1 || queue[0].ClientRequestID != "original-request" {
		t.Fatalf("queue: %+v %v", queue, err)
	}
	status, accepted = promptJSON(t, hs, id, "", map[string]any{"queueId": queue[0].ID, "clientRequestId": "must-not-replace"})
	if status != 202 || accepted["clientRequestId"] != "original-request" {
		t.Fatalf("promoted: %d %+v", status, accepted)
	}
	stream.unblock()
	st := srv.runAt(id)
	<-st.done
	st.mu.Lock()
	defer st.mu.Unlock()
	found := map[loop.EventType]bool{}
	for _, event := range st.evs {
		if event == nil || event.Message == nil || event.Message.Text() != "same text" {
			continue
		}
		if event.Message.ClientRequestID != "original-request" {
			t.Fatalf("event identity changed: %+v", event)
		}
		if event.Type == loop.SteerAccepted || event.Type == loop.MessageEnd {
			found[event.Type] = true
		}
	}
	if len(found) != 2 {
		data, _ := json.Marshal(found)
		t.Fatalf("accepted/end identity projection: %s", data)
	}
}
