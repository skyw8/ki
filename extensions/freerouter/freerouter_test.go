package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ki/internal/state"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"OPENROUTER_API_KEY", "FREEROUTER_API_KEY", "OPENROUTER_BASE_URL", "FREEROUTER_LISTEN", "FREEROUTER_RACE_WIDTH", "FREEROUTER_MAX_BATCHES"} {
		t.Setenv(key, "")
	}
}
func decodeObject(t *testing.T, text string) object {
	t.Helper()
	var value object
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func TestDefaults(t *testing.T) {
	c := defaultConfig()
	if c.Listen != defaultListen || c.RaceWidth != 2 {
		t.Fatalf("defaults: %+v", c)
	}
}
func TestConfigPrecedenceAndClamps(t *testing.T) {
	clearEnv(t)
	root := t.TempDir()
	saved := object{"version": 1, "apiKey": "file", "baseUrl": " http://file/v1/// ", "listen": " file:99 ", "raceWidth": 99, "maxBatches": 0, "slowTtlMs": 1, "idleTimeoutMs": 99999999}
	if err := state.WriteVersioned(filepath.Join(root, "config.json"), 1, saved, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "env")
	t.Setenv("OPENROUTER_BASE_URL", "http://env/")
	t.Setenv("FREEROUTER_LISTEN", "127.0.0.1:44")
	c := loadSidecar(root)
	if c.APIKey != "file" || c.BaseURL != "http://file/v1" || c.Listen != "127.0.0.1:44" || c.RaceWidth != 8 || c.MaxBatches != 3 || c.SlowTTLMS != 1000 || c.IdleTimeoutMS != 600000 {
		t.Fatalf("config: %+v", c)
	}
	t.Setenv("FREEROUTER_RACE_WIDTH", "0")
	if got := loadSidecar(root).RaceWidth; got != 2 {
		t.Fatalf("zero race override: %d", got)
	}
	t.Setenv("FREEROUTER_RACE_WIDTH", "7")
	t.Setenv("FREEROUTER_MAX_BATCHES", "99")
	c = loadStandalone(" cli:88 ", " http://cli/// ")
	if c.APIKey != "env" || c.BaseURL != "http://cli" || c.Listen != "cli:88" || c.RaceWidth != 7 || c.MaxBatches != 6 {
		t.Fatalf("standalone: %+v", c)
	}
	if resolveAPIKey("config", " credential ") != "credential" || resolveAPIKey(" config ", "") != "config" || resolveAPIKey("", "") != "env" {
		t.Fatal("key precedence")
	}
	if err := state.WriteJSON(filepath.Join(root, "config.json"), object{"version": 2, "apiKey": "future"}, 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadSidecar(root).APIKey; got != "env" {
		t.Fatalf("newer document fallback: %s", got)
	}
}
func TestEnvironmentAPIKeyFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("FREEROUTER_API_KEY", " fallback ")
	// Like the Rust implementation, presence of the primary variable is decisive
	// even when it is empty; fallback applies when the primary variable is absent.
	if envAPIKey() != "" {
		t.Fatal("empty primary")
	}
	if err := os.Unsetenv("OPENROUTER_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if envAPIKey() != "fallback" {
		t.Fatal("fallback")
	}
}
func TestPreferenceOrderAndCooldown(t *testing.T) {
	r := newFreeRouter([]string{"a", "b", "c"}, 50, 10)
	if got := r.nextModels(10); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatal(got)
	}
	r.markExhausted("b")
	if got := r.nextModels(10); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatal(got)
	}
	e := r.exhausted["b"]
	e.at = time.Now().Add(-60 * time.Millisecond)
	r.exhausted["b"] = e
	if got := r.nextModels(10); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatal(got)
	}
}
func TestMarkSlowDoesNotDowngrade(t *testing.T) {
	r := newFreeRouter([]string{"a"}, 1000, 10)
	r.markExhausted("a")
	r.markSlow("a")
	if len(r.nextModels(10)) != 0 || r.exhausted["a"].ttl != time.Second {
		t.Fatal("cooldown downgraded")
	}
	r.markExhausted("missing")
	r.setModels([]string{"b"})
	if len(r.exhausted) != 0 {
		t.Fatal("stale cooldown")
	}
	r.markSlow("b")
	if r.exhausted["b"].ttl != 10*time.Millisecond {
		t.Fatal("slow ttl")
	}
}
func TestConvertsBasicIR(t *testing.T) {
	req := decodeObject(t, `{"system":"sys","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"toolCall","id":"1","name":"Bash","arguments":{"command":"ls"}}]},{"role":"toolResult","toolCallId":"1","content":[{"type":"text","text":"out"}]}],"tools":[{"name":"Bash","description":"d","parameters":{"type":"object"}}],"maxTokens":100}`)
	input := kiRequestToChat(req)
	if str(obj(input.Messages[0])["role"]) != "system" || obj(input.Messages[1])["content"] != "hi" || obj(input.Messages[2])["tool_calls"] == nil || obj(input.Messages[3])["role"] != "tool" || len(input.Tools) != 1 || input.MaxTokens != 100 {
		t.Fatalf("converted: %s", jsonText(input))
	}
}
func TestIRImagesReasoningAndRawArguments(t *testing.T) {
	req := decodeObject(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","data":"abc","mimeType":"image/jpeg"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"thought"},{"type":"toolCall","id":"a","name":"f","arguments":{"x":1},"argumentsRaw":"{ \"x\": 2 }"},{"type":"toolCall","id":"b","name":"g","arguments":{"y":3},"argumentsRaw":"[]"}]},{"role":"toolResult","toolCallId":"a","content":[{"type":"image","data":"x"}]}]}`)
	messages := toOpenRouterMessages(req)
	parts := arr(obj(messages[0])["content"])
	if obj(obj(parts[1])["image_url"])["url"] != "data:image/jpeg;base64,abc" {
		t.Fatal(parts)
	}
	assistant := obj(messages[1])
	calls := arr(assistant["tool_calls"])
	if assistant["reasoning_content"] != "thought" || obj(obj(calls[0])["function"])["arguments"] != "{ \"x\": 2 }" || obj(obj(calls[1])["function"])["arguments"] != `{"y":3}` {
		t.Fatal(assistant)
	}
	if obj(messages[2])["content"] != "(see attached image)" {
		t.Fatal(messages[2])
	}
}
func TestFiltersAndSorts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("request: %s", r.URL)
		}
		fmt.Fprint(w, `{"data":[{"id":"deepseek/deepseek-v3:free","context_length":64000},{"id":"groq/llama:free","name":"Llama","context_length":32000,"top_provider":{"max_completion_tokens":8192}},{"id":"meta/vision:free"},{"id":"paid/model"},{"id":"groq/other:free","context_length":1000}]}`)
	}))
	defer server.Close()
	models, err := fetchFreeModels(context.Background(), server.Client(), "test", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(modelIDs(models), []string{"groq/other:free", "groq/llama:free", "deepseek/deepseek-v3:free"}) || models[0].MaxTokens != 4096 || models[1].Name != "Llama" || models[1].MaxTokens != 8192 {
		t.Fatal(models)
	}
	for _, id := range []string{"x/content-safety:free", "x/MODERATION:free", "x/guard:free", "x/model-vl:free", "x/vl:free", "x/vision:free"} {
		if isGeneralAssistant(id) {
			t.Fatal(id)
		}
	}
}
func TestDiscoverySingleFlightAndRefreshFailure(t *testing.T) {
	var count atomic.Int32
	gate := make(chan struct{})
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"a:free"}]}`)
	}))
	defer server.Close()
	d := &modelDiscovery{client: server.Client()}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := d.ensure(context.Background(), "key", server.URL); errors <- err }()
	}
	for deadline := time.Now().Add(time.Second); count.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	close(gate)
	wg.Wait()
	for i := 0; i < 8; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 1 {
		t.Fatalf("requests=%d", count.Load())
	}
	fail.Store(true)
	if d.refresh(context.Background(), "key", server.URL) == nil || len(d.cached()) != 1 {
		t.Fatal("refresh clobbered cache")
	}
}
func TestDiscoveryErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{{"http", "", 503, "503 Service Unavailable"}, {"invalid", "x", 200, "Invalid models response"}, {"empty", `{"data":[]}`, 200, "No free models"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer s.Close()
			_, err := fetchFreeModels(context.Background(), s.Client(), "key", s.URL)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	if _, err := fetchFreeModels(context.Background(), http.DefaultClient, "", "ignored"); err == nil {
		t.Fatal("missing key")
	}
}
func TestStreamParserTextToolsUsageAndUTF8(t *testing.T) {
	chunks := []string{`{"choices":[{"delta":{"content":""}}]}`, `{"choices":[{"delta":{"reasoning":"ignored"}}]}`, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool1","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}],"content":"你好"}}]}`, `{"choices":[{"delta":{"content":"!"},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`}
	var events []object
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Title") != "freerouter" || r.Header.Get("Authorization") != "Bearer key" {
			t.Error("upstream headers")
		}
		var body object
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true || uintValue(body["max_tokens"]) != 123 || len(arr(body["tools"])) != 1 {
			t.Error(body)
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\r\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	err := streamFreeModel(context.Background(), s.Client(), "a:free", raceInput{Messages: []any{}, Tools: []any{object{"name": "f"}}, MaxTokens: 123}, "key", s.URL, func(ev object) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	final := obj(events[len(events)-1]["message"])
	content := arr(final["content"])
	if len(content) != 2 || str(obj(content[0])["type"]) != "toolCall" || uintValue(obj(obj(content[0])["arguments"])["a"]) != 1 || obj(content[1])["text"] != "你好!" || final["stopReason"] != "toolUse" || uintValue(obj(final["usage"])["totalTokens"]) != 7 {
		t.Fatal(final)
	}
	types := []string{}
	for _, ev := range events {
		types = append(types, str(ev["type"]))
	}
	want := []string{"start", "toolcall_start", "text_start", "text_delta", "toolcall_delta", "text_delta", "text_end", "toolcall_end", "done"}
	if !reflect.DeepEqual(types, want) {
		t.Fatal(types)
	}
	for _, ev := range events {
		if strings.HasPrefix(str(ev["type"]), "text_") && uintValue(ev["contentIndex"]) != 1 {
			t.Fatal(ev)
		}
	}
}
func TestStreamErrorsAndClosingBlocks(t *testing.T) {
	for _, status := range []int{400, 402, 422, 429, 500, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer s.Close()
			var events []object
			err := streamFreeModel(context.Background(), s.Client(), "m:free", raceInput{}, "key", s.URL, func(e object) { events = append(events, e) })
			if err == nil || len(events) != 1 || events[0]["type"] != "error" {
				t.Fatal(err, events)
			}
			e := err.(*modelError)
			want := "exhausted"
			if status == 402 {
				want = "fatal"
			}
			if status == 401 {
				want = "other"
			}
			if e.Kind != want {
				t.Fatal(e)
			}
		})
	}
	var events []object
	p := newStreamParser("m:free", func(e object) { events = append(events, e) })
	_ = p.handle(decodeObject(t, `{"choices":[{"delta":{"content":"partial","tool_calls":[{"id":"a","function":{"name":"f","arguments":"invalid"}}]}}]}`))
	err := p.handle(object{"error": object{"status": 402, "message": "credits"}})
	p.closeBlocks()
	if err == nil || err.(*modelError).Kind != "fatal" || events[len(events)-1]["type"] != "toolcall_end" || obj(obj(events[len(events)-1]["toolCall"])["arguments"])["_raw"] != "invalid" {
		t.Fatal(err, events)
	}
}
func TestQualifyingEvents(t *testing.T) {
	for _, ev := range []object{event("text_start", nil), event("toolcall_start", nil), event("done", object{"message": object{"content": []any{object{"type": "text", "text": "x"}}}})} {
		if !qualifies(ev) {
			t.Fatal(ev)
		}
	}
	for _, ev := range []object{event("start", nil), event("done", object{"message": object{"content": []any{object{"type": "thinking", "thinking": "x"}, object{"type": "text", "text": ""}}}})} {
		if qualifies(ev) {
			t.Fatal(ev)
		}
	}
	for batch, want := range map[int]uint64{1: 101, 2: 152, 3: 202, 6: 202} {
		if got := batchTimeoutMS(config{FirstTokenTimeoutMS: 101}, batch); got != want {
			t.Fatal(batch, got, want)
		}
	}
}

type fakeBehavior struct {
	status       int
	delay        time.Duration
	empty, stall bool
	inlineError  int
}
type fakeUpstream struct {
	server    *httptest.Server
	mu        sync.Mutex
	requests  []string
	cancelled chan string
}

func newFakeUpstream(t *testing.T, ids []string, behaviors map[string]fakeBehavior) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{cancelled: make(chan string, 32)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			data := []any{}
			for _, id := range ids {
				data = append(data, object{"id": id})
			}
			_ = json.NewEncoder(w).Encode(object{"data": data})
			return
		}
		var body object
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		id := str(body["model"])
		f.mu.Lock()
		f.requests = append(f.requests, id)
		f.mu.Unlock()
		b := behaviors[id]
		if b.delay > 0 {
			select {
			case <-time.After(b.delay):
			case <-r.Context().Done():
				f.cancelled <- id
				return
			}
		}
		if b.status != 0 {
			w.WriteHeader(b.status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if b.inlineError != 0 {
			fmt.Fprintf(w, "data: %s\n\n", jsonText(object{"error": object{"code": b.inlineError, "message": "inline failure"}}))
			return
		}
		if !b.empty {
			fmt.Fprintf(w, "data: %s\n\n", jsonText(object{"choices": []any{object{"delta": object{"content": id}}}}))
		}
		if b.stall {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			f.cancelled <- id
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(f.server.Close)
	return f
}
func testPool(t *testing.T, f *fakeUpstream, mutate func(*config)) *pool {
	t.Helper()
	c := defaultConfig()
	c.APIKey = "key"
	c.BaseURL = f.server.URL
	c.FirstTokenTimeoutMS = 10000
	c.IdleTimeoutMS = 50
	if mutate != nil {
		mutate(&c)
	}
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	return newPool(ctx, c, f.server.Client())
}
func collectRace(ctx context.Context, p *pool, input raceInput, sidecar bool) []object {
	var events []object
	runRace(ctx, p, input, p.resolveKey(""), func(ev object) { events = append(events, ev) }, sidecar)
	return events
}
func finalEvent(events []object) object {
	if len(events) == 0 {
		return nil
	}
	return events[len(events)-1]
}
func TestRaceFastestWinsAndCancelsLoser(t *testing.T) {
	f := newFakeUpstream(t, []string{"slow:free", "fast:free"}, map[string]fakeBehavior{"slow:free": {delay: time.Second}, "fast:free": {delay: 5 * time.Millisecond}})
	p := testPool(t, f, nil)
	events := collectRace(context.Background(), p, raceInput{Messages: []any{}}, true)
	final := finalEvent(events)
	if final["type"] != "done" || obj(final["message"])["model"] != "fast:free" {
		t.Fatal(events)
	}
	content := arr(obj(final["message"])["content"])
	thought := str(obj(content[0])["thinking"])
	if !strings.Contains(thought, "Searching free models...\nRound 1: slow:free, fast:free\nUsing fast:free") {
		t.Fatal(thought)
	}
	for _, ev := range events {
		if str(ev["type"]) == "text_start" && uintValue(ev["contentIndex"]) != 1 {
			t.Fatal(ev)
		}
	}
	select {
	case id := <-f.cancelled:
		if id != "slow:free" {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("loser not cancelled")
	}
}
func TestRaceFailoverAndExhaustionCooldown(t *testing.T) {
	f := newFakeUpstream(t, []string{"a:free", "b:free", "c:free"}, map[string]fakeBehavior{"a:free": {status: 429}, "b:free": {status: 400}})
	p := testPool(t, f, nil)
	events := collectRace(context.Background(), p, raceInput{}, false)
	final := finalEvent(events)
	if final["type"] != "done" || obj(final["message"])["model"] != "c:free" {
		t.Fatal(events)
	}
	p.mu.Lock()
	available := p.router.nextModels(10)
	p.mu.Unlock()
	if !reflect.DeepEqual(available, []string{"c:free"}) {
		t.Fatal(available)
	}
	events = collectRace(context.Background(), p, raceInput{}, false)
	if obj(finalEvent(events)["message"])["model"] != "c:free" {
		t.Fatal(events)
	}
}
func TestRaceEmptyResponseCannotWin(t *testing.T) {
	f := newFakeUpstream(t, []string{"empty:free", "real:free"}, map[string]fakeBehavior{"empty:free": {empty: true}, "real:free": {delay: 10 * time.Millisecond}})
	p := testPool(t, f, nil)
	events := collectRace(context.Background(), p, raceInput{}, false)
	if obj(finalEvent(events)["message"])["model"] != "real:free" {
		t.Fatal(events)
	}
}
func TestRaceTimeoutAdvancesBatchAndUsesSlowCooldown(t *testing.T) {
	f := newFakeUpstream(t, []string{"slow:free", "fast:free"}, map[string]fakeBehavior{"slow:free": {delay: time.Second}})
	p := testPool(t, f, func(c *config) { c.RaceWidth = 1; c.FirstTokenTimeoutMS = 100 })
	events := collectRace(context.Background(), p, raceInput{}, false)
	if obj(finalEvent(events)["message"])["model"] != "fast:free" {
		t.Fatal(events)
	}
	p.mu.Lock()
	e := p.router.exhausted["slow:free"]
	p.mu.Unlock()
	if e.ttl != milliseconds(p.snapshot().SlowTTLMS) {
		t.Fatal(e)
	}
}
func TestRaceFatalAccountErrorAndExhausted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		behavior fakeBehavior
		contains string
		rounds   int
	}{{"credits", fakeBehavior{status: 402}, "insufficient credits", 1}, {"inline", fakeBehavior{inlineError: 402}, "inline failure", 1}, {"exhausted", fakeBehavior{status: 503}, "All free models exhausted", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeUpstream(t, []string{"a:free", "b:free"}, map[string]fakeBehavior{"a:free": tc.behavior, "b:free": tc.behavior})
			p := testPool(t, f, func(c *config) { c.RaceWidth = 1 })
			events := collectRace(context.Background(), p, raceInput{}, false)
			final := finalEvent(events)
			if final["type"] != "error" || !strings.Contains(str(final["error"]), tc.contains) {
				t.Fatal(events)
			}
			f.mu.Lock()
			n := len(f.requests)
			f.mu.Unlock()
			if n != tc.rounds {
				t.Fatalf("rounds %d", n)
			}
		})
	}
}
func TestRaceIdleTimeoutAndCancellation(t *testing.T) {
	f := newFakeUpstream(t, []string{"a:free"}, map[string]fakeBehavior{"a:free": {stall: true}})
	p := testPool(t, f, func(c *config) { c.IdleTimeoutMS = 10 })
	events := collectRace(context.Background(), p, raceInput{}, false)
	if !strings.Contains(str(finalEvent(events)["error"]), "stream stalled") {
		t.Fatal(events)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var cancelled []object
	runRace(ctx, p, raceInput{}, "key", func(ev object) {
		cancelled = append(cancelled, ev)
		if str(ev["type"]) == "text_start" {
			cancel()
		}
	}, false)
	if finalEvent(cancelled)["reason"] != "aborted" {
		t.Fatal(cancelled)
	}
}
func TestPinnedModelAndMissingKey(t *testing.T) {
	f := newFakeUpstream(t, []string{"listed:free"}, nil)
	p := testPool(t, f, nil)
	events := collectRace(context.Background(), p, raceInput{PinnedModel: "pinned:free"}, true)
	if obj(finalEvent(events)["message"])["model"] != "pinned:free" || str(obj(arr(obj(finalEvent(events)["message"])["content"])[0])["thinking"]) != "Using pinned:free" {
		t.Fatal(events)
	}
	var missing []object
	runRace(context.Background(), p, raceInput{}, "", func(ev object) { missing = append(missing, ev) }, true)
	if len(missing) != 1 || !strings.Contains(str(missing[0]["error"]), "No OpenRouter API key") {
		t.Fatal(missing)
	}
}
func TestHTTPProxyJSONSSEModelsAndGzip(t *testing.T) {
	f := newFakeUpstream(t, []string{"a:free"}, nil)
	p := testPool(t, f, nil)
	server := httptest.NewServer(httpRouter(p))
	defer server.Close()
	response, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var models object
	_ = json.NewDecoder(response.Body).Decode(&models)
	response.Body.Close()
	if len(arr(models["data"])) != 1 {
		t.Fatal(models)
	}
	response, err = http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"auto","messages":[],"max_tokens":10}`))
	if err != nil {
		t.Fatal(err)
	}
	var completion object
	_ = json.NewDecoder(response.Body).Decode(&completion)
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-Freerouter-Model") != "a:free" || completion["model"] != "a:free" || uintValue(obj(completion["usage"])["total_tokens"]) != 5 {
		t.Fatal(response.StatusCode, completion)
	}
	response, err = http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(response.Body).Decode(&models)
	response.Body.Close()
	if len(arr(models["data"])) != 2 {
		t.Fatal(models)
	}
	response, err = http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"auto","messages":[],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("Cache-Control") != "no-cache" || !strings.Contains(string(raw), `"content":"a:free"`) || !strings.Contains(string(raw), `"model":"a:free"`) || !strings.HasSuffix(string(raw), "data: [DONE]\n\n") {
		t.Fatal(response.Header, string(raw))
	}
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/healthz", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Content-Encoding") != "gzip" || response.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal(response.Header)
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(reader)
	reader.Close()
	response.Body.Close()
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatal(string(raw))
	}
}
func TestHTTPValidationAndErrorStatuses(t *testing.T) {
	clearEnv(t)
	for _, tc := range []struct {
		name, body     string
		key            bool
		upstreamStatus int
		status         int
		want           string
	}{{"no-key", `{"messages":[]}`, false, 0, 401, "authentication_error"}, {"unknown", `{"model":"paid","messages":[]}`, true, 0, 400, "invalid_request_error"}, {"missing-messages", `{}`, true, 0, 422, "messages is required"}, {"exhausted", `{"messages":[]}`, true, 429, 503, "freerouter_exhausted"}, {"fatal", `{"messages":[]}`, true, 402, 502, "freerouter_error"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeUpstream(t, []string{"a:free"}, map[string]fakeBehavior{"a:free": {status: tc.upstreamStatus}})
			p := testPool(t, f, nil)
			if !tc.key {
				c := p.snapshot()
				c.APIKey = ""
				p.updateConfig(c)
			}
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer ignored")
			w := httptest.NewRecorder()
			httpRouter(p).ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
func TestHTTPToolCompletionAndSSEFields(t *testing.T) {
	message := object{"content": []any{object{"type": "thinking", "thinking": "ignored"}, object{"type": "toolCall", "id": "a", "name": "f", "arguments": object{"x": 1}}}, "stopReason": "toolUse", "usage": object{"input": 1, "output": 2, "totalTokens": 3}}
	result := assistantToChatCompletion(message, "m:free")
	choice := obj(arr(result["choices"])[0])
	msg := obj(choice["message"])
	if msg["content"] != nil || choice["finish_reason"] != "tool_calls" || obj(obj(arr(msg["tool_calls"])[0])["function"])["arguments"] != `{"x":1}` {
		t.Fatal(result)
	}
	start := sseValue(event("toolcall_start", object{"toolCallId": "a", "toolName": "f"}))
	if !strings.Contains(jsonText(start), `"arguments":""`) {
		t.Fatal(start)
	}
	delta := sseValue(event("toolcall_delta", object{"delta": "{}"}))
	if !strings.Contains(jsonText(delta), `"arguments":"{}"`) {
		t.Fatal(delta)
	}
}
func TestSidecarInitializeStreamCancelReloadAndShutdown(t *testing.T) {
	clearEnv(t)
	f := newFakeUpstream(t, []string{"a:free"}, map[string]fakeBehavior{"a:free": {stall: true}})
	p := testPool(t, f, func(c *config) { c.IdleTimeoutMS = 5000 })
	root := t.TempDir()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	messages := make(chan object, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var msg object
			if json.Unmarshal(sc.Bytes(), &msg) == nil {
				messages <- msg
			}
		}
		close(messages)
	}()
	done := make(chan error, 1)
	go func() { done <- runSidecar(ctx, p, root, inR, outW); outW.Close() }()
	t.Cleanup(func() { inW.Close(); outR.Close(); cancel() })
	send := func(id int, method string, params object) {
		t.Helper()
		_, err := fmt.Fprintf(inW, "%s\n", jsonText(object{"jsonrpc": "2.0", "id": id, "method": method, "params": params}))
		if err != nil {
			t.Fatal(err)
		}
	}
	next := func() object {
		t.Helper()
		select {
		case msg := <-messages:
			if msg == nil {
				t.Fatal("sidecar closed")
			}
			return msg
		case <-time.After(2 * time.Second):
			t.Fatal("sidecar timeout")
			return nil
		}
	}
	send(1, "initialize", nil)
	init := next()
	if uintValue(init["id"]) != 1 || obj(init["result"])["fallback"] != false {
		t.Fatal(init)
	}
	status := next()
	if status["method"] != "ui.setGlobalStatus" || obj(status["params"])["tone"] != "success" {
		t.Fatal(status)
	}
	send(2, "provider.stream.start", object{"requestId": "r", "request": object{"credential": object{"apiKey": "host-key"}, "request": object{"messages": []any{object{"role": "user", "content": []any{object{"type": "text", "text": "hello"}}}}}}})
	accepted := next()
	if uintValue(accepted["id"]) != 2 || obj(accepted["result"])["accepted"] != true {
		t.Fatal(accepted)
	}
	for {
		m := next()
		params := obj(m["params"])
		if params["requestId"] != "r" {
			t.Fatal(m)
		}
		if params["type"] == "text_start" {
			if uintValue(params["contentIndex"]) != 1 {
				t.Fatal(m)
			}
			break
		}
	}
	if p.resolveKey("") != "key" {
		t.Fatal("config key must precede last credential")
	}
	c := p.snapshot()
	c.APIKey = ""
	p.updateConfig(c)
	if p.resolveKey("") != "key" {
		t.Fatal("last resolved key")
	}
	send(3, "provider.stream.cancel", object{"requestId": "r"})
	sawAck, sawAbort := false, false
	for !sawAck || !sawAbort {
		m := next()
		if uintValue(m["id"]) == 3 {
			sawAck = true
		}
		if obj(m["params"])["reason"] == "aborted" {
			sawAbort = true
		}
	}
	if err := state.WriteVersioned(filepath.Join(root, "config.json"), 1, object{"version": 1, "apiKey": "new", "raceWidth": 5}, 0600); err != nil {
		t.Fatal(err)
	}
	send(4, "config.updated", nil)
	if uintValue(next()["id"]) != 4 || p.snapshot().RaceWidth != 5 {
		t.Fatal("reload")
	}
	send(5, "unknown", nil)
	if code := obj(next()["error"])["code"]; code != float64(-32601) {
		t.Fatalf("method not found code: %v", code)
	}
	send(6, "shutdown", nil)
	if uintValue(next()["id"]) != 6 {
		t.Fatal("shutdown acknowledgement")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
}
func TestSidecarEOFCancelsStreamsAndCredentialSharedWithHTTP(t *testing.T) {
	clearEnv(t)
	f := newFakeUpstream(t, []string{"a:free"}, nil)
	p := testPool(t, f, func(c *config) { c.APIKey = "" })
	input := bytes.NewBufferString(jsonText(object{"jsonrpc": "2.0", "id": 1, "method": "initialize"}) + "\n" + jsonText(object{"jsonrpc": "2.0", "id": 2, "method": "shutdown"}) + "\n")
	var out bytes.Buffer
	if err := runSidecar(context.Background(), p, t.TempDir(), input, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 3 {
		t.Fatal(out.String())
	}
	streamInput := bytes.NewBufferString(jsonText(object{"jsonrpc": "2.0", "id": 3, "method": "provider.stream.start", "params": object{"requestId": "eof", "request": object{"credential": object{"apiKey": "credential"}, "request": object{"messages": []any{}}}}}) + "\n")
	out.Reset()
	if err := runSidecar(context.Background(), p, t.TempDir(), streamInput, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"reason":"aborted"`) || !strings.Contains(out.String(), `"requestId":"eof"`) {
		t.Fatal(out.String())
	}
	if p.resolveKey(" credential ") != "credential" || p.resolveKey("") != "credential" {
		t.Fatal("shared credential")
	}
}

func TestCancelledRacePrecedesQueuedCompletionAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   modelEvent
		timeout bool
	}{
		{name: "winner completion", event: modelEvent{index: 0, finished: true, result: &modelError{Kind: "cancelled"}}},
		{name: "last candidate completion", event: modelEvent{index: 2, finished: true, result: &modelError{Kind: "cancelled"}}},
		{name: "winner aborted event", event: modelEvent{index: 0, event: streamError("aborted", "cancelled")}},
		{name: "simultaneous timeout", event: modelEvent{index: 0, finished: true}, timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			events := make(chan modelEvent, 1)
			events <- tc.event
			var deadline chan time.Time
			if tc.timeout {
				deadline = make(chan time.Time, 1)
				deadline <- time.Now()
			}
			_, timedOut, cancelled := nextRaceEvent(ctx, events, deadline)
			if !cancelled || timedOut {
				t.Fatalf("cancelled=%v timedOut=%v", cancelled, timedOut)
			}
		})
	}
}
func TestRaceReceiveRetainsUncancelledCompletionAndTimeout(t *testing.T) {
	events := make(chan modelEvent, 1)
	events <- modelEvent{index: 4, finished: true, result: &modelError{Kind: "exhausted", ModelID: "model"}}
	got, timedOut, cancelled := nextRaceEvent(t.Context(), events, nil)
	if got.index != 4 || !got.finished || got.result == nil || timedOut || cancelled {
		t.Fatal(got, timedOut, cancelled)
	}
	deadline := make(chan time.Time, 1)
	deadline <- time.Now()
	_, timedOut, cancelled = nextRaceEvent(t.Context(), nil, deadline)
	if !timedOut || cancelled {
		t.Fatal(timedOut, cancelled)
	}
}
