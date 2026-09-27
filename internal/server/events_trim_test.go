package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"ki/internal/loop"
	"ki/internal/types"
)

func TestEventsWireSnapshotsPatchesAndResume(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	st := &runState{runID: "wire", done: make(chan struct{})}
	st.wait = sync.NewCond(&st.mu)
	for i := 1; i <= 40; i++ {
		event := assistantDelta(strings.Repeat("中文🙂", i*50))
		event.Seq = int64(i * 3)
		st.evs = append(st.evs, &event)
	}
	close(st.done)
	srv.mu.Lock()
	srv.runs[id] = st
	srv.mu.Unlock()
	for _, cursor := range []string{"", "wire:60"} {
		res := openEventsWithCursor(t, hs, id, cursor)
		scanner := bufio.NewScanner(res.Body)
		var decoder loop.MessageDecoder
		count, patches, wireBytes := 0, 0, 0
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			wireBytes += len(line)
			var event loop.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if count == 0 && event.Message == nil {
				t.Fatal("first/resumed frame is not a snapshot")
			}
			if event.MessagePatch != nil {
				patches++
			}
			decoded, err := decoder.Decode(event)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Message.Text() != strings.Repeat("中文🙂", int(event.Seq/3)*50) {
				t.Fatal("decoded wire differs from canonical message")
			}
			count++
		}
		_ = res.Body.Close()
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		want := 40
		if cursor != "" {
			want = 20
		}
		if count != want || patches != want-1 {
			t.Fatalf("frames %d patches %d", count, patches)
		}
		if wireBytes > 50_000 {
			t.Fatalf("repeated partial on wire: %d bytes", wireBytes)
		}
		t.Logf("cursor=%q frames=%d bytes=%d", cursor, count, wireBytes)
	}
}

// assistantDelta builds one streaming update whose partial carries the whole
// text so far, exactly like the provider adapters do.
func assistantDelta(text string) loop.Event {
	m := types.Message{Role: "assistant", Content: []types.Content{{Type: "text", Text: text}}}
	return loop.Event{Type: loop.MessageUpdate, Message: &m, AssistantMessageEvent: &loop.AssistantDelta{Type: "text_delta", Delta: text, Partial: m}}
}

// multiDeltaStreamer answers every request with one assistant message split
// into n streaming chunks, so a test can drive the replay trimming end to end.
type multiDeltaStreamer struct{ n int }

func (s *multiDeltaStreamer) Stream(_ context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	m := types.Message{Role: "assistant", StopReason: "stop", Provider: req.Provider, Model: req.Model}
	for i := 1; i <= s.n; i++ {
		m.Content = []types.Content{{Type: "text", Text: strings.Repeat("z", i)}}
		if err := emit(loop.AssistantDelta{Type: "text_delta", Delta: "z", Partial: m}); err != nil {
			return m, err
		}
	}
	return m, nil
}

// TestEmitterTrimsSupersededDeltas: with no reader attached the replay log
// keeps only the newest streaming partial. Every superseded chunk carried the
// whole accumulated message, so keeping them made the buffer quadratic in a
// long turn's output (and made every re-attach resend it).
func TestEmitterTrimsSupersededDeltas(t *testing.T) {
	em, _, _ := newEmitterForTest(t)
	for i := 0; i < 5; i++ {
		if err := em.Emit(assistantDelta(strings.Repeat("x", i+1))); err != nil {
			t.Fatal(err)
		}
	}
	em.st.mu.Lock()
	defer em.st.mu.Unlock()
	if len(em.st.evs) != 5 {
		t.Fatalf("buffered events = %d, want 5", len(em.st.evs))
	}
	for i, ev := range em.st.evs {
		if want := i < 4; ev.Blank != want {
			t.Fatalf("event %d blank = %v, want %v", i, ev.Blank, want)
		}
		// A blanked slot carries no payload (and shares the blank value), so its
		// seq is gone; readers skip it before any cursor comparison.
		if !ev.Blank && ev.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d", i, ev.Seq)
		}
	}
	newest := em.st.evs[4]
	if newest.Blank || newest.Message == nil || newest.Message.Text() != "xxxxx" {
		t.Fatalf("newest partial trimmed: %+v", newest)
	}
	// Blanked slots share one value, so a long run's trimmed chunks cost a
	// pointer each instead of a 424-byte loop.Event.
	if em.st.evs[0] != em.st.evs[1] {
		t.Fatal("blanked slots must share the blank payload")
	}
}

// TestEmitterReplayStaysBounded is the reason the trimming exists: a long
// streaming turn must not grow the replay buffer with its accumulated output.
// Every chunk repeats the whole partial, so keeping them all was quadratic in
// the turn's text (a 52-minute, 200-round run in the wild held ~200 MB).
func TestEmitterReplayStaysBounded(t *testing.T) {
	const chunks = 200
	em, _, _ := newEmitterForTest(t)
	for i := 1; i <= chunks; i++ {
		if err := em.Emit(assistantDelta(strings.Repeat("z", i*512))); err != nil {
			t.Fatal(err)
		}
	}
	em.st.mu.Lock()
	defer em.st.mu.Unlock()
	var retained, blanked int
	for _, ev := range em.st.evs {
		if ev.Blank {
			blanked++
			continue
		}
		if ev.Message != nil {
			retained += len(ev.Message.Text())
		}
	}
	if want := chunks * 512; retained != want {
		t.Fatalf("retained %d chars, want only the newest partial (%d)", retained, want)
	}
	if blanked != chunks-1 {
		t.Fatalf("blanked %d of %d chunks", blanked, chunks)
	}
}

// TestEmitterKeepsDeltasALiveReaderIsOwed: within the retention caps, trimming
// still only drops a payload every attached reader has already passed, so a
// reader that is following along (the CLI) keeps its chunks.
func TestEmitterKeepsDeltasALiveReaderIsOwed(t *testing.T) {
	em, _, _ := newEmitterForTest(t)
	reader := &runReader{}
	em.st.addReader(reader)
	for i := 0; i < 5; i++ {
		if err := em.Emit(assistantDelta(fmt.Sprintf("chunk-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	em.st.mu.Lock()
	blanks := 0
	for _, ev := range em.st.evs {
		if ev.Blank {
			blanks++
		}
	}
	em.st.mu.Unlock()
	if blanks != 0 {
		t.Fatalf("blanked %d events a reader had not read", blanks)
	}

	// The reader catches up to index 3; only the payloads it already consumed
	// may go when the next chunk arrives.
	em.st.mu.Lock()
	reader.pos = 3
	em.st.mu.Unlock()
	if err := em.Emit(assistantDelta("chunk-5")); err != nil {
		t.Fatal(err)
	}
	em.st.mu.Lock()
	defer em.st.mu.Unlock()
	for i, ev := range em.st.evs {
		if want := i < 3; ev.Blank != want {
			t.Fatalf("event %d blank = %v, want %v", i, ev.Blank, want)
		}
	}
}

// TestEmitterTrimsBehindAStalledReader is why the retention caps exist: a reader
// that stopped consuming (a backgrounded tab, a suspended phone page, a
// port-forward) used to pin every superseded payload for the whole run. In the
// wild that reached 14,766 buffered events / 286 MB of repeated partials, ~1 GB
// of daemon RSS, and a fresh page load replayed all of it before it could paint.
func TestEmitterTrimsBehindAStalledReader(t *testing.T) {
	const chunks = maxKeptSuperseded * 3
	em, _, _ := newEmitterForTest(t)
	stalled := &runReader{}
	em.st.addReader(stalled) // never consumes: pos stays 0
	for i := 1; i <= chunks; i++ {
		// Every chunk repeats the whole partial, as the provider adapters do.
		if err := em.Emit(assistantDelta(strings.Repeat("z", i*16))); err != nil {
			t.Fatal(err)
		}
	}
	// The in-flight partial is the newest slot; a further chunk shows the buffer
	// stays bounded rather than growing with the run.
	if err := em.Emit(assistantDelta("tail")); err != nil {
		t.Fatal(err)
	}
	em.st.mu.Lock()
	defer em.st.mu.Unlock()
	live, bytes := 0, 0
	for _, ev := range em.st.evs {
		if ev.Blank {
			continue
		}
		if ev.Type != loop.MessageUpdate {
			t.Fatalf("unexpected live event %s", ev.Type)
		}
		live++
		bytes += eventPayloadBytes(ev)
	}
	// The in-flight partial plus at most maxKeptSuperseded superseded ones.
	if live > maxKeptSuperseded+1 {
		t.Fatalf("kept %d live payloads behind a stalled reader, want <= %d", live, maxKeptSuperseded+1)
	}
	if bytes > maxKeptSupersededBytes {
		t.Fatalf("kept %d bytes behind a stalled reader, want <= %d", bytes, maxKeptSupersededBytes)
	}
	if live < 2 {
		t.Fatalf("dropped too much: %d live payloads", live)
	}
	if len(em.st.evs) != chunks+1 {
		t.Fatalf("buffer grew to %d slots", len(em.st.evs))
	}
}

func TestEmitterCountsToolArgumentsInStalledReaderBudget(t *testing.T) {
	em, _, _ := newEmitterForTest(t)
	em.st.addReader(&runReader{})
	for i := 0; i < 20; i++ {
		message := types.Message{Role: "assistant", Content: []types.Content{{Type: "toolCall", ID: "call", Name: "Write", Arguments: map[string]any{"content": strings.Repeat("x", 512<<10)}}}}
		if err := em.Emit(loop.Event{Type: loop.MessageUpdate, Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
	em.st.mu.Lock()
	defer em.st.mu.Unlock()
	bytes := 0
	for i, ev := range em.st.evs {
		if ev.Blank || i == em.st.partial {
			continue
		}
		if ev.BufferedBytes < 512<<10 {
			t.Fatalf("tool payload undercounted: %d", ev.BufferedBytes)
		}
		bytes += eventPayloadBytes(ev)
	}
	if bytes > maxKeptSupersededBytes {
		t.Fatalf("superseded tool payload: %d", bytes)
	}
}

// TestEmitterTrimsASupersededMessageGroupOnceTheRunIsLong: the caps count
// payloads, not messages, so a run with many short messages stays bounded too.
func TestEmitterTrimsBehindAStalledReaderAcrossMessages(t *testing.T) {
	em, _, _ := newEmitterForTest(t)
	stalled := &runReader{}
	em.st.addReader(stalled)
	for i := 0; i < 40; i++ {
		if err := em.Emit(assistantDelta(fmt.Sprintf("message-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	em.st.mu.Lock()
	defer em.st.mu.Unlock()
	live := 0
	for _, ev := range em.st.evs {
		if !ev.Blank {
			live++
		}
	}
	if live > maxKeptSuperseded+1 {
		t.Fatalf("kept %d payloads, want <= %d", live, maxKeptSuperseded+1)
	}
}

// TestRunStateTrimsAClosedMessageGroup: a message's start and its newest
// partial stop being the "in-flight" state once the message is persisted — the
// transcript a reader can fetch already has them. Replaying the message_end
// alone is enough, which is what lets a resume skip a long turn's text.
func TestRunStateTrimsAClosedMessageGroup(t *testing.T) {
	em, _, _ := newEmitterForTest(t)
	st := em.st
	live := func() []loop.EventType {
		t.Helper()
		var kinds []loop.EventType
		for _, ev := range st.evs {
			if !ev.Blank {
				kinds = append(kinds, ev.Type)
			}
		}
		return kinds
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	start := loop.Event{Type: loop.MessageStart, Message: &types.Message{Role: "assistant"}}
	st.appendLocked(&start)
	for i := 0; i < 3; i++ {
		d := assistantDelta(strings.Repeat("x", i+1))
		st.appendLocked(&d)
	}
	// Still in flight: the start is superseded, the newest partial is kept.
	if got, want := live(), []loop.EventType{loop.MessageUpdate}; !slices.Equal(got, want) {
		t.Fatalf("in-flight replay = %v, want %v", got, want)
	}

	end := loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: "assistant"}, EntryID: "e1"}
	st.appendLocked(&end)
	if got, want := live(), []loop.EventType{loop.MessageEnd}; !slices.Equal(got, want) {
		t.Fatalf("closed replay = %v, want %v", got, want)
	}

	// A message_end that was not persisted (no entry id) is not reconstructible
	// from the transcript, so its group must stay for a reader that arrives
	// later.
	loose := loop.Event{Type: loop.MessageStart, Message: &types.Message{Role: "assistant"}}
	st.appendLocked(&loose)
	st.appendLocked(&loop.Event{Type: loop.MessageEnd, Message: &types.Message{Role: "assistant"}})
	if got, want := live(), []loop.EventType{loop.MessageEnd, loop.MessageStart, loop.MessageEnd}; !slices.Equal(got, want) {
		t.Fatalf("unpersisted replay = %v, want %v", got, want)
	}
}

// TestEmitterCoalescesRepeatedRequestHeaders: every round of a run repeats the
// same system prompt and tool schemas, which was ~7 MB of a 9 MB buffer in a
// 200-round run. The repeat is stored as promptUnchanged with no body, the same
// shape the persisted entry already uses, while the live event keeps its payload
// for the context-usage estimate that runs after the buffer stage.
func TestEmitterCoalescesRepeatedRequestHeaders(t *testing.T) {
	em, _, _ := newEmitterForTest(t)
	header := func(system string) {
		t.Helper()
		if err := em.Emit(loop.Event{Type: loop.RequestHeader, System: system, Tools: []loop.ToolSpec{{Name: "Read"}}}); err != nil {
			t.Fatal(err)
		}
	}
	header("sys")
	header("sys")
	header("sys-changed")

	em.st.mu.Lock()
	defer em.st.mu.Unlock()
	var headers, usages []*loop.Event
	for _, ev := range em.st.evs {
		switch ev.Type {
		case loop.RequestHeader:
			headers = append(headers, ev)
		case loop.ContextUsage:
			usages = append(usages, ev)
		}
	}
	if len(headers) != 3 || len(usages) != 3 {
		t.Fatalf("headers %d usages %d", len(headers), len(usages))
	}
	if headers[0].PromptUnchanged || headers[0].System != "sys" || len(headers[0].Tools) != 1 {
		t.Fatalf("first header must carry its body: %+v", headers[0])
	}
	if !headers[1].PromptUnchanged || headers[1].System != "" || headers[1].Tools != nil {
		t.Fatalf("repeat header was not coalesced: %+v", headers[1])
	}
	if headers[2].PromptUnchanged || headers[2].System != "sys-changed" {
		t.Fatalf("changed header lost its body: %+v", headers[2])
	}
	// The estimate reads the caller's event, not the stored copy: a coalesced
	// header must still count the system prompt and schemas it stands for, so
	// the repeat estimates the same context as the original.
	if usages[0].UsedTokens <= 0 || usages[0].UsedTokens != usages[1].UsedTokens {
		t.Fatalf("context usage changed with coalescing: %d vs %d", usages[0].UsedTokens, usages[1].UsedTokens)
	}
}

// TestEventsReplayOmitsSupersededDeltas is the end-to-end counterpart: a run
// that streamed four chunks is replayed to a later reader as one message_update
// (the newest partial), not four.
func TestEventsReplayOmitsSupersededDeltas(t *testing.T) {
	srv, hs := testServerWith(t, &multiDeltaStreamer{n: 4})
	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "hi")
	waitRunEnd(t, srv, id)

	//nolint:bodyclose // collectSSE owns and closes the response body.
	replay := collectSSE(t, mustOpenEvents(t, hs, id))
	updates := 0
	for _, label := range replay {
		if label == "message_update" {
			updates++
		}
	}
	if updates != 0 {
		t.Fatalf("replay carried %d message_update events, want none (all superseded):\n%v", updates, replay)
	}
	if want := wantReplay(); !slices.Equal(replay, want) {
		t.Fatalf("replay:\n got %v\nwant %v", replay, want)
	}
}

// readEventIDs reads a whole SSE response and returns its "id:" lines in order,
// so a test can resume from one.
func readEventIDs(t *testing.T, res *http.Response) []string {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	var ids []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "id:") {
			continue
		}
		ids = append(ids, strings.TrimSpace(strings.TrimPrefix(line, "id:")))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func openEventsWithCursor(t *testing.T, hs *httptest.Server, id, cursor string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions/"+id+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Last-Event-ID", cursor)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestEventsResumeCursor: the id line carries "<runID>:<seq>", and sending it
// back as Last-Event-ID resumes the stream after that event instead of
// replaying the whole run. A cursor from another run is ignored.
func TestEventsResumeCursor(t *testing.T) {
	srv, hs := testServer(t)
	id := createSession(t, hs, t.TempDir())
	prompt202(t, hs, id, "hi")
	waitRunEnd(t, srv, id)
	//nolint:bodyclose // readEventIDs owns and closes the response body.
	ids := readEventIDs(t, mustOpenEvents(t, hs, id))
	if want := wantReplay(); len(ids) != len(want) {
		t.Fatalf("ids = %v", ids)
	}

	//nolint:bodyclose // collectSSE owns and closes the response body.
	got := collectSSE(t, openEventsWithCursor(t, hs, id, ids[2]))
	if want := wantReplay()[3:]; !slices.Equal(got, want) {
		t.Fatalf("resumed stream:\n got %v\nwant %v", got, want)
	}

	// A cursor naming a different run cannot skip this run's events.
	//nolint:bodyclose // collectSSE owns and closes the response body.
	foreign := collectSSE(t, openEventsWithCursor(t, hs, id, "run-other:99"))
	if want := wantReplay(); !slices.Equal(foreign, want) {
		t.Fatalf("foreign cursor stream: %v", foreign)
	}
}
