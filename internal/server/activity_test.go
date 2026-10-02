package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/session"
)

func TestCountActiveDescendants(t *testing.T) {
	tests := []struct {
		name   string
		infos  []session.Info
		active map[string]bool
		want   map[string]int
	}{
		{
			name: "transitive mixed fork modes and workspaces",
			infos: []session.Info{
				{ID: "root", CWD: "one"},
				{ID: "child", ParentSessionID: "root", ForkMode: session.ForkModeTree, CWD: "one"},
				{ID: "grandchild", ParentSessionID: "child", ForkMode: session.ForkModeFlat, CWD: "two"},
				{ID: "sibling", ParentSessionID: "root"},
				{ID: "unrelated"},
			},
			active: map[string]bool{"root": true, "child": true, "grandchild": true, "sibling": false, "unrelated": true},
			want:   map[string]int{"root": 2, "child": 1, "grandchild": 0, "sibling": 0, "unrelated": 0},
		},
		{
			name: "orphan preserves its own subtree",
			infos: []session.Info{
				{ID: "orphan", ParentSessionID: "missing"},
				{ID: "child", ParentSessionID: "orphan"},
				{ID: "root"},
			},
			active: map[string]bool{"orphan": true, "child": true, "missing": true},
			want:   map[string]int{"orphan": 1, "child": 0, "root": 0},
		},
		{
			name: "cycle counts each descendant once without self",
			infos: []session.Info{
				{ID: "a", ParentSessionID: "c"},
				{ID: "b", ParentSessionID: "a"},
				{ID: "c", ParentSessionID: "b"},
				{ID: "branch", ParentSessionID: "a"},
				{ID: "leaf", ParentSessionID: "branch"},
				{ID: "unrelated"},
			},
			active: map[string]bool{"a": true, "b": true, "branch": true, "leaf": true, "unrelated": true},
			want:   map[string]int{"a": 3, "b": 3, "c": 4, "branch": 1, "leaf": 0, "unrelated": 0},
		},
		{
			name: "self cycle",
			infos: []session.Info{
				{ID: "self", ParentSessionID: "self"},
				{ID: "child", ParentSessionID: "self"},
			},
			active: map[string]bool{"self": true, "child": true},
			want:   map[string]int{"self": 1, "child": 0},
		},
		{
			name:   "unknown runs have no durable edges",
			infos:  []session.Info{{ID: "root"}},
			active: map[string]bool{"unknown": true},
			want:   map[string]int{"root": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countActiveDescendants(tt.infos, tt.active); !maps.Equal(got, tt.want) {
				t.Fatalf("counts = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountActiveDescendantsDeepChain(t *testing.T) {
	const size = 20000
	infos := make([]session.Info, size)
	active := make(map[string]bool, size)
	for i := range infos {
		id := fmt.Sprint(i)
		infos[i] = session.Info{ID: id}
		if i != 0 {
			infos[i].ParentSessionID = infos[i-1].ID
		}
		active[id] = true
	}
	counts := countActiveDescendants(infos, active)
	for i, info := range infos {
		if counts[info.ID] != size-i-1 {
			t.Fatalf("count[%s] = %d, want %d", info.ID, counts[info.ID], size-i-1)
		}
	}
}

func TestSessionActivityListAndDetail(t *testing.T) {
	streamer := newSteerAgentStreamer()
	srv, hs := testServerWith(t, streamer)
	unblock := sync.OnceFunc(func() { close(streamer.release) })
	t.Cleanup(unblock)
	root := createSession(t, hs, t.TempDir())
	fork := func(parent, mode string) string {
		t.Helper()
		status, out := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+parent+"/fork", map[string]any{"forkMode": mode})
		if status != http.StatusOK {
			t.Fatalf("fork: status=%d body=%v", status, out)
		}
		return out["id"].(string)
	}
	child := fork(root, session.ForkModeTree)
	grandchild := fork(child, session.ForkModeFlat)
	unrelated := createSession(t, hs, t.TempDir())

	type wantActivity struct {
		running bool
		count   int
	}
	assert := func(want map[string]wantActivity) string {
		t.Helper()
		rows, etag := activityList(t, hs, "")
		if len(rows) != len(want) {
			t.Fatalf("list has %d rows, want %d", len(rows), len(want))
		}
		for id, expected := range want {
			check := func(where string, row map[string]any) {
				t.Helper()
				if row["running"] != expected.running || row["activeDescendantCount"] != float64(expected.count) {
					t.Fatalf("%s %s: running=%v descendants=%v; want %v/%d",
						where, id, row["running"], row["activeDescendantCount"], expected.running, expected.count)
				}
			}
			check("list", rows[id])
			check("detail", sessionTailGET(t, hs, id))
			check("runtime detail", sessionGETFields(t, hs, id, "runtime"))
		}
		return etag
	}
	want := map[string]wantActivity{
		root: {}, child: {}, grandchild: {}, unrelated: {},
	}
	idleTag := assert(want)
	events := pushEvents(t, hs, "tok")
	waitPush(t, events, "ready", func(ev pushEvent) bool { return ev.Type == "ready" })

	prompt202(t, hs, grandchild, "activity-grandchild")
	waitPush(t, events, "descendant occupy invalidation", isInvalidate(scopeSessions))
	select {
	case <-streamer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("descendant never reached its model call")
	}
	want[root], want[child] = wantActivity{count: 1}, wantActivity{count: 1}
	want[grandchild] = wantActivity{running: true}
	activeTag := assert(want)
	if activeTag == idleTag {
		t.Fatal("starting a descendant did not change the list ETag")
	}
	activityList(t, hs, activeTag)

	prompt202(t, hs, child, "activity-child")
	prompt202(t, hs, unrelated, "activity-unrelated")
	want[root], want[child], want[unrelated] = wantActivity{count: 2}, wantActivity{running: true, count: 1}, wantActivity{running: true}
	assert(want)

	// Descendant activity is a display projection, not parent run ownership:
	// an idle parent can start its own real run while its children are active.
	prompt202(t, hs, root, "activity-root")
	want[root] = wantActivity{running: true, count: 2}
	assert(want)

	abort := func(id string) {
		t.Helper()
		status, out := postJSON(t, hs, http.MethodPost, "/v1/sessions/"+id+"/abort", nil)
		if status != http.StatusOK {
			t.Fatalf("abort: status=%d body=%v", status, out)
		}
		waitRunEnd(t, srv, id)
	}
	abort(root)
	want[root] = wantActivity{count: 2}
	assert(want)
	abort(child)
	want[root], want[child] = wantActivity{count: 1}, wantActivity{count: 1}
	assert(want)

	// A fresh subscriber sees both completion and release invalidation. Scopes
	// coalesce and have writer priority, so invalidate can arrive before the
	// already-queued agent_end; waiting sequentially would discard that frame.
	// Retained completed runs must no longer contribute to ancestor counts.
	settled := pushEvents(t, hs, "tok")
	waitPush(t, settled, "ready", func(ev pushEvent) bool { return ev.Type == "ready" })
	unblock()
	var ended, invalidated bool
	waitPush(t, settled, "descendant completion and release invalidation", func(ev pushEvent) bool {
		ended = ended || ev.Type == loop.AgentEnd && ev.SessionID == grandchild
		invalidated = invalidated || isInvalidate(scopeSessions)(ev)
		return ended && invalidated
	})
	waitRunEnd(t, srv, grandchild)
	waitRunEnd(t, srv, unrelated)
	for id := range want {
		want[id] = wantActivity{}
	}
	assert(want)
}

func activityList(t *testing.T, hs *httptest.Server, etag string) (map[string]map[string]any, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("If-None-Match", etag)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if etag != "" {
		if res.StatusCode != http.StatusNotModified {
			t.Fatalf("unchanged activity list: status=%d", res.StatusCode)
		}
		return nil, res.Header.Get("ETag")
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("activity list: status=%d", res.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		byID[row["id"].(string)] = row
	}
	return byID, res.Header.Get("ETag")
}
