package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
	"ki/internal/tools"
	"ki/internal/types"
)

// The actual child run, TaskOutput tool and parent loop share these gates. This
// reproduces completion enqueue-before-Wait-return without timing sleeps.
type completionRaceStreamer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *completionRaceStreamer) Stream(ctx context.Context, req loop.Request, _ func(loop.AssistantDelta) error) (types.Message, error) {
	for _, m := range req.Messages {
		if strings.Contains(m.Text(), "You are a subagent") {
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

type observedWaitRuntime struct {
	*Server
	waiting chan struct{}
}

func (r observedWaitRuntime) Wait(ctx context.Context, id string) (tools.TaskSnapshot, error) {
	close(r.waiting)
	return r.Server.Wait(ctx, id)
}

func outputTool(t *testing.T, runtime tools.AgentRuntime, caller ...string) loop.Tool {
	t.Helper()
	set := tools.Set{Agent: runtime, CWD: t.TempDir()}
	if len(caller) > 0 {
		set.AgentParentSessionID = caller[0]
	}
	for _, tool := range set.Build(tools.Profile{}) {
		if tool.Name() == "TaskOutput" {
			return tool
		}
	}
	t.Fatal("TaskOutput missing")
	return nil
}

func TestCompletionDeliverySiblingReadCannotClaimParentResult(t *testing.T) {
	for _, parentPulls := range []bool{false, true} {
		name := "notification"
		if parentPulls {
			name = "parent-pull"
		}
		t.Run(name, func(t *testing.T) {
			stream := &completionRaceStreamer{started: make(chan struct{}), release: make(chan struct{})}
			srv, hs := testServerWith(t, stream)
			parentID := createSession(t, hs, t.TempDir())
			parent, err := srv.open(parentID)
			if err != nil {
				t.Fatal(err)
			}
			sibling, err := srv.newAgentChild(parent, false)
			_ = parent.Close()
			if err != nil {
				t.Fatal(err)
			}
			siblingID := sibling.ID()
			srv.sidx.Add(siblingID, sibling.Dir)
			_ = sibling.Close()
			st, runCtx, err := srv.occupy(t.Context(), parentID)
			if err != nil {
				t.Fatal(err)
			}
			enableRunInbox(st)
			launch, err := srv.SpawnAgent(t.Context(), tools.AgentRequest{
				ParentSessionID: parentID, Prompt: "gated child", RunInBackground: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			<-stream.started
			close(stream.release)
			snapshot, err := srv.Wait(t.Context(), launch.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.ParentSessionID != parentID {
				t.Fatalf("snapshot lost owner: %+v", snapshot)
			}
			args := map[string]any{"task_id": launch.TaskID, "block": false}
			result := outputTool(t, srv, siblingID).Execute(t.Context(), args)
			if result.IsError || !strings.Contains(result.Content[0].Text, `"read_only":true`) || !strings.Contains(result.Content[0].Text, "child result") {
				t.Fatalf("sibling inspection was not read-only: %+v", result)
			}
			wantNotifications := 1
			if parentPulls {
				result = outputTool(t, srv, parentID).Execute(t.Context(), args)
				if result.IsError || strings.Contains(result.Content[0].Text, `"read_only":true`) {
					t.Fatalf("sibling consumed parent's claim: %+v", result)
				}
				wantNotifications = 0
			}
			srv.runPrompt(runCtx, st, parentID, []types.Content{{Type: "text", Text: "parent"}}, nil, "", "", "", nil)
			if got := completionCount(t, srv, parentID); got != wantNotifications {
				t.Fatalf("notifications=%d, want %d after sibling inspection", got, wantNotifications)
			}
			result = outputTool(t, srv, parentID).Execute(t.Context(), args)
			if result.IsError || !strings.Contains(result.Content[0].Text, `"read_only":true`) {
				t.Fatalf("parent repeat was not read-only: %+v", result)
			}
		})
	}
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
				t.Fatalf("lost delivery identity: %+v", m)
			}
		}
	}
	return count
}

func TestCompletionDeliveryRuntimeRace(t *testing.T) {
	for _, mode := range []string{"live-pull", "queue-pull", "handoff-pull", "notification-first", "timeout", "cancel", "terminal-cancel"} {
		t.Run(mode, func(t *testing.T) {
			stream := &completionRaceStreamer{started: make(chan struct{}), release: make(chan struct{})}
			srv, hs := testServerWith(t, stream)
			parent := createSession(t, hs, t.TempDir())
			st, runCtx, err := srv.occupy(t.Context(), parent)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "queue-pull" {
				enableRunInbox(st)
			}
			launch, err := srv.SpawnAgent(t.Context(), tools.AgentRequest{
				ParentSessionID: parent, Prompt: "gated child", Description: "completion race", RunInBackground: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			<-stream.started
			runtime := observedWaitRuntime{Server: srv, waiting: make(chan struct{})}
			tool := outputTool(t, runtime, parent)
			args := map[string]any{"task_id": launch.TaskID, "block": true, "timeout": 5000}
			var first loop.ToolResult
			pulled := strings.HasSuffix(mode, "-pull")
			if pulled {
				done := make(chan loop.ToolResult, 1)
				go func() { done <- tool.Execute(t.Context(), args) }()
				<-runtime.waiting
				close(stream.release)
				first = <-done
			} else {
				if mode == "timeout" {
					args["timeout"] = 0
					first = tool.Execute(t.Context(), args)
					if !strings.Contains(first.Content[0].Text, `"retrieval_status":"timeout"`) {
						t.Fatalf("timeout: %+v", first)
					}
					if !strings.Contains(first.Content[0].Text, launch.TaskID) {
						t.Fatalf("timeout lost task snapshot across scoped runtime: %+v", first)
					}
				}
				if mode == "cancel" {
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					first = tool.Execute(ctx, args)
					if !first.IsError {
						t.Fatal("cancelled TaskOutput succeeded")
					}
				}
				close(stream.release)
				if _, err := srv.Wait(t.Context(), launch.TaskID); err != nil {
					t.Fatal(err)
				}
				if mode == "terminal-cancel" {
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					first = tool.Execute(ctx, args)
					if !first.IsError {
						t.Fatal("cancelled terminal TaskOutput consumed result")
					}
				}
			}
			if pulled && (first.IsError || !strings.Contains(first.Content[0].Text, "child result") || strings.Contains(first.Content[0].Text, `"read_only":true`)) {
				t.Fatalf("first pull must own the result: %+v", first)
			}
			if mode == "handoff-pull" {
				srv.preserveRunInbox(parent, st)
				dir, _ := srv.sidx.Lookup(parent)
				items, err := session.ReadQueue(dir)
				if err != nil || len(items) != 1 || items[0].Completion == nil {
					t.Fatalf("handoff lost generation: %+v %v", items, err)
				}
				enableRunInbox(st)
			}
			if mode == "queue-pull" {
				enableRunInbox(st)
			}
			srv.runPrompt(runCtx, st, parent, []types.Content{{Type: "text", Text: "parent"}}, nil, "", "", "", nil)
			want := 1
			if pulled {
				want = 0
			}
			if got := completionCount(t, srv, parent); got != want {
				t.Fatalf("committed notifications=%d, want %d", got, want)
			}
			// Repeated reads are explicitly read-only regardless of whether the
			// original owner was a tool result or a committed notification.
			repeated := outputTool(t, srv, parent).Execute(t.Context(), map[string]any{"task_id": launch.TaskID, "block": false})
			if repeated.IsError || !strings.Contains(repeated.Content[0].Text, `"read_only":true`) {
				t.Fatalf("repeated read: %+v", repeated)
			}
			st.mu.Lock()
			defer st.mu.Unlock()
			for _, ev := range st.evs {
				if ev != nil && ev.Type == loop.SteerAccepted && ev.Message != nil && ev.Message.Completion != nil {
					t.Fatal("uncommitted completion leaked optimistic acceptance")
				}
			}
		})
	}
}

func TestCompletionLiveHandoffKeepsUnreadGeneration(t *testing.T) {
	stream := &completionRaceStreamer{started: make(chan struct{}), release: make(chan struct{})}
	srv, hs := testServerWith(t, stream)
	parent := createSession(t, hs, t.TempDir())
	st, _, err := srv.occupy(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	enableRunInbox(st)
	launch, err := srv.SpawnAgent(t.Context(), tools.AgentRequest{ParentSessionID: parent, Prompt: "child", RunInBackground: true})
	if err != nil {
		t.Fatal(err)
	}
	<-stream.started
	close(stream.release)
	if _, err := srv.Wait(t.Context(), launch.TaskID); err != nil {
		t.Fatal(err)
	}
	srv.preserveRunInbox(parent, st)
	srv.release(parent, st)
	next := srv.runAt(parent)
	select {
	case <-next.done:
	case <-time.After(5 * time.Second):
		t.Fatal("handoff did not wake parent")
	}
	if got := completionCount(t, srv, parent); got != 1 {
		t.Fatalf("handoff committed %d notifications", got)
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
