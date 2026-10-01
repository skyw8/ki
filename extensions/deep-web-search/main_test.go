package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ki/internal/state"
	"ki/pkg/extensionrpc"
)

func testApp(t *testing.T) *searchApp {
	t.Helper()
	a := newApp(t.TempDir(), extensionrpc.New(io.Discard))
	a.call = func(context.Context, string, any, any) error { return nil }
	t.Cleanup(func() { a.cancel(); a.bg.Wait() })
	return a
}
func invoke(t *testing.T, a *searchApp, name string, args obj) obj {
	t.Helper()
	raw, _ := json.Marshal(obj{"name": name, "args": args, "sessionId": "s1"})
	out, e := a.handle(t.Context(), "tool.execute", raw)
	if e != nil {
		t.Fatal(e)
	}
	return out.(obj)
}
func outputText(out obj) string { return str(record(array(out["content"])[0])["text"]) }
func TestRuntimeRegistrationAndEmptyQuery(t *testing.T) {
	a := testApp(t)
	out, e := a.handle(t.Context(), "initialize", json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	names := []string{}
	for _, v := range array(out.(obj)["tools"]) {
		names = append(names, str(record(v)["name"]))
	}
	if !reflect.DeepEqual(names, []string{"deep_web_search", "fetch_content", "get_search_content", "source_check"}) {
		t.Fatal(names)
	}
	result := invoke(t, a, "deep_web_search", obj{})
	if result["isError"] != true || !strings.Contains(outputText(result), "requires query") {
		t.Fatal(result)
	}
}
func TestWorkflowIsSetting(t *testing.T) {
	spec := record(toolSpecs[0])
	parameters := record(spec["parameters"])
	if record(parameters["properties"])["workflow"] != nil || parameters["additionalProperties"] != false || strings.Contains(strings.ToLower(str(spec["description"])), "curator") || strings.Contains(str(spec["description"]), "summary-review") {
		t.Fatal(spec)
	}
}
func TestAllProviderTogglesBeforeNetwork(t *testing.T) {
	a := testApp(t)
	cfg := obj{"version": 1, "providerToggles": obj{"codex": false, "exa": false, "tinyfish": false, "duckduckgo": false}}
	if e := state.WriteVersioned(filepath.Join(a.root, "config.json"), 1, cfg, 0600); e != nil {
		t.Fatal(e)
	}
	a.search = func(context.Context, string, string, obj, obj) (obj, error) {
		t.Fatal("network access despite toggles")
		return nil, nil
	}
	result := invoke(t, a, "deep_web_search", obj{"query": "toggle test"})
	if result["isError"] != true || !strings.Contains(outputText(result), "disabled") {
		t.Fatal(result)
	}
}
func TestBudgets(t *testing.T) {
	if searchBudget("codex") < 60*time.Second || searchBudget("exa") >= searchBudget("codex") || queryBudget <= searchBudget("codex") || toolBudget <= queryBudget {
		t.Fatal("budget hierarchy")
	}
}
func TestSettleWithGrace(t *testing.T) {
	resolved := func(s string) func() (any, error) { return func() (any, error) { return s, nil } }
	t.Run("all first", func(t *testing.T) {
		got := settleWithGrace(t.Context(), []func() (any, error){resolved("a"), resolved("b")}, 2, 50*time.Millisecond, nil)
		if got[0].Value != "a" || got[1].Value != "b" {
			t.Fatal(got)
		}
	})
	t.Run("cut straggler", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		cut := false
		got := settleWithGrace(t.Context(), []func() (any, error){resolved("a"), resolved("b"), func() (any, error) { <-release; return "late", nil }}, 2, 30*time.Millisecond, func() { cut = true })
		if got[0].Value != "a" || got[1].Value != "b" || got[2].Present || !cut {
			t.Fatal(got, cut)
		}
	})
	t.Run("wait quorum", func(t *testing.T) {
		got := settleWithGrace(t.Context(), []func() (any, error){resolved("fast"), func() (any, error) { time.Sleep(40 * time.Millisecond); return "slow", nil }}, 2, 5*time.Millisecond, nil)
		if got[0].Value != "fast" || got[1].Value != "slow" {
			t.Fatal(got)
		}
	})
	t.Run("rejection", func(t *testing.T) {
		got := settleWithGrace(t.Context(), []func() (any, error){func() (any, error) { return nil, errors.New("boom") }}, 1, 10*time.Millisecond, nil)
		if !got[0].Present || got[0].Err == nil || got[0].Err.Error() != "boom" {
			t.Fatal(got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if len(settleWithGrace(t.Context(), nil, 1, time.Millisecond, nil)) != 0 {
			t.Fatal("empty batch")
		}
	})
}
func TestTimeoutHonorsBudgetAndParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	time.Sleep(30 * time.Millisecond)
	if ctx.Err() == nil {
		t.Fatal("deadline")
	}
	parent, abort := context.WithCancel(t.Context())
	child, stop := context.WithTimeout(parent, 10*time.Second)
	defer stop()
	abort()
	if child.Err() == nil {
		t.Fatal("upstream cancellation")
	}
}
func TestNormalization(t *testing.T) {
	if got := normalizeQueries(obj{"queries": []any{" one ", "one", 3, "two"}, "query": "ignored"}); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatal(got)
	}
	if got := canonical("HTTPS://Example.COM:443/path/?utm_source=x&keep=1#fragment"); got != "https://example.com/path?keep=1" {
		t.Fatal(got)
	}
	include, exclude := domains([]any{"example.com", "https://Example.com/a", "-https://bad.example.com/x", "invalid"})
	if !reflect.DeepEqual(include, []string{"example.com"}) || !reflect.DeepEqual(exclude, []string{"bad.example.com"}) {
		t.Fatal(include, exclude)
	}
	cfg := normalizeConfig(obj{"provider": "wrong", "maxResults": 99, "summaryGenerationDeadlineMs": 1})
	if cfg["provider"] != "all" || cfg["maxResults"] != 20 || cfg["summaryGenerationDeadlineMs"] != 1000 {
		t.Fatal(cfg)
	}
	if _, e := normalizeOptions(obj{"proxy": "socks://host"}, cfg); e == nil {
		t.Fatal("proxy accepted")
	}
}
func TestRankingDedupAndContent(t *testing.T) {
	runs := []any{obj{"provider": "exa", "results": []any{obj{"url": "https://a.com/page?utm_source=x", "title": "One exact source", "snippet": "alpha", "content": "body"}, obj{"url": "https://b.com/two", "title": "Other page", "snippet": "beta"}}}, obj{"provider": "duckduckgo", "results": []any{obj{"url": "https://a.com/page", "title": "One exact source", "snippet": "more"}, obj{"url": "https://c.com/three", "title": "A third unique source", "snippet": "gamma"}}}}
	got := mergeResults(runs, 5)
	if len(got) != 3 {
		t.Fatal(got)
	}
	first := record(got[0])
	if first["url"] != "https://a.com/page" || len(stringList(first["providers"])) != 2 || first["content"] != "body" || number(first["score"]) < .03 {
		t.Fatal(first)
	}
}
func TestProviderIsolationAndSummaryFallback(t *testing.T) {
	a := testApp(t)
	a.config = normalizeConfig(obj{"provider": "exa", "workflow": "auto-summary", "summaryModel": "unsupported/model"})
	a.search = func(ctx context.Context, provider, query string, options, config obj) (obj, error) {
		if query == "fail" {
			return nil, errors.New("network failure")
		}
		return obj{"provider": provider, "results": []any{obj{"title": "Source", "url": "https://source.com/one", "snippet": "A source with useful trustworthy evidence."}}}, nil
	}
	out := invoke(t, a, "deep_web_search", obj{"queries": []any{"good", "fail"}})
	if out["isError"] == true || !strings.Contains(outputText(out), "Sources: 1") {
		t.Fatal(out)
	}
	details := record(out["details"])
	if details["effectiveWorkflow"] != "none" || details["fallbackTo"] != "none" || len(array(details["queryFailures"])) != 1 {
		t.Fatal(details)
	}
	id := str(details["responseId"])
	cached := invoke(t, a, "get_search_content", obj{"responseId": id})
	if cached["isError"] == true || !strings.Contains(outputText(cached), "[no content]") {
		t.Fatal(cached)
	}
}
func TestExaAPIAndMCPFallback(t *testing.T) {
	var mu sync.Mutex
	routes := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		routes = append(routes, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Path == "/answer" {
			if r.Header.Get("x-api-key") != "secret" {
				t.Error("missing key")
			}
			fmt.Fprint(w, `{"answer":"answer","citations":[{"title":"Exa source","url":"https://example.com/x","text":"passage"}]}`)
			return
		}
		var rpc obj
		_ = json.NewDecoder(r.Body).Decode(&rpc)
		if str(record(rpc["params"])["name"]) == "web_search_advanced_exa" {
			fmt.Fprint(w, `{"error":{"message":"unsupported"}}`)
			return
		}
		fmt.Fprint(w, "data: {\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Title: MCP source\\nURL: https://example.com/mcp\\nText: fetched passage\"}]}}\n\n")
	}))
	defer server.Close()
	t.Setenv("EXA_BASE_URL", server.URL)
	t.Setenv("KI_EXA_MCP_URL", server.URL+"/mcp")
	options, _ := normalizeOptions(obj{}, normalizeConfig(nil))
	result, e := searchExa(t.Context(), "query", options, obj{"exaApiKey": "secret", "exaMode": "api"})
	if e != nil || result["transport"] != "api" || result["answer"] != "answer" {
		t.Fatal(result, e)
	}
	options["includeContent"] = true
	result, e = searchExa(t.Context(), "query", options, obj{"exaMode": "mcp"})
	if e != nil || result["transport"] != "mcp" || len(array(result["inlineContent"])) != 1 {
		t.Fatal(result, e)
	}
	if len(routes) != 3 {
		t.Fatal(routes)
	}
}
func TestTinyfishAndDuckduckgo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			if r.Header.Get("X-API-Key") != "tiny" || r.URL.Query().Get("include_domains") != "example.com" || r.URL.Query().Get("recency_minutes") != "1440" {
				t.Error(r.URL, r.Header)
			}
			fmt.Fprint(w, `{"results":[{"url":"https://example.com/x","title":"tiny","snippet":"passage"}]}`)
		} else if r.URL.Path == "/fetch" {
			fmt.Fprint(w, `{"results":[{"url":"https://example.com/x","text":""},{"url":"https://example.com/y","text":"body"}]}`)
		} else {
			fmt.Fprint(w, `<div class="result"><a class="result__a" href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fx">A &amp; B</a><a class="result__snippet">The source snippet.</a></div>`)
		}
	}))
	defer server.Close()
	t.Setenv("KI_TINYFISH_SEARCH_URL", server.URL+"/search")
	t.Setenv("KI_TINYFISH_FETCH_URL", server.URL+"/fetch")
	t.Setenv("KI_DUCKDUCKGO_URL", server.URL+"/html")
	config := obj{"tinyfishApiKey": "tiny"}
	options := obj{"domainFilter": []any{"example.com"}, "recencyFilter": "day", "numResults": 5}
	result, e := searchTinyfish(t.Context(), "query", options, config)
	if e != nil || len(array(result["results"])) != 1 {
		t.Fatal(result, e)
	}
	content, e := fetchTinyfish(t.Context(), []string{"https://example.com/x"}, config)
	if e != nil || len(content) != 1 {
		t.Fatal(content, e)
	}
	result, e = searchDuckduckgo(t.Context(), "query", options)
	if e != nil || record(array(result["results"])[0])["title"] != "A & B" {
		t.Fatal(result, e)
	}
}
func TestContentSafetyAndHTML(t *testing.T) {
	for _, u := range []string{"file:///tmp/x", "http://localhost/x", "http://127.0.0.1/x", "http://10.0.0.1/x", "http://192.168.0.1/x", "http://[::1]/x", "https://user:password@example.com/"} {
		if _, e := safeFetchURL(u); e == nil {
			t.Errorf("accepted %s", u)
		}
	}
	if _, e := safeFetchURL("https://example.com/"); e != nil {
		t.Fatal(e)
	}
	title, body := htmlToText(`<html><title>A &amp; B</title><script>secret</script><p>Visible</p><p>Next</p></html>`)
	if title != "A & B" || strings.Contains(body, "secret") || !strings.Contains(body, "Visible\nNext") {
		t.Fatal(title, body)
	}
	result := fetchOneContent(t.Context(), "http://localhost/secret")
	if str(result["content"]) != "" || !strings.Contains(str(result["error"]), "blocked") {
		t.Fatal(result)
	}
}
func TestEvidenceAssessments(t *testing.T) {
	evidence := buildEvidence(obj{"query": "q", "responseId": "id", "results": []any{obj{"url": "https://github.com/a/b", "title": "Primary", "snippet": "A quantum processor uses qubits for computing."}, obj{"url": "https://example.com/negative", "title": "Contradiction", "content": "A quantum processor never uses classical bits for computing."}}})
	sources := array(evidence["sources"])
	if record(record(sources[0])["classification"])["kind"] != "primary" {
		t.Fatal(sources)
	}
	got := assessClaims([]string{"quantum processor uses qubits", "quantum processor uses classical bits", "missing zoology hypothesis"}, evidence)
	if record(got[2])["status"] != "missing-evidence" || record(got[1])["status"] != "contradicted" {
		t.Fatal(got)
	}
}
func TestCacheExpiryAndFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := &searchCache{path: path}
	c.put("key", obj{"hello": "world"})
	value := c.get("key", time.Minute)
	value["hello"] = "changed"
	if c.get("key", time.Minute)["hello"] != "world" {
		t.Fatal("cache alias")
	}
	c.mu.Lock()
	v := c.entries["key"]
	v.CreatedAt = time.Now().Add(-time.Hour).UnixMilli()
	c.entries["key"] = v
	c.mu.Unlock()
	if c.get("key", time.Minute) != nil {
		t.Fatal("expired cache")
	}
	newer := []byte(`{"version":999,"entries":{}}`)
	if e := os.WriteFile(path, newer, 0600); e != nil {
		t.Fatal(e)
	}
	c.put("new", obj{"a": 1})
	raw, _ := os.ReadFile(path)
	if string(raw) != string(newer) {
		t.Fatal("overwrote newer cache")
	}
}
func TestCodexResponseSourcesAndCredentialRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	doc := obj{"version": 1, "providers": obj{"openai-codex": obj{"type": "oauth", "value": obj{"access": "old", "refresh": "refresh", "accountId": "account", "expires": 1}}, "other": obj{"type": "api-key", "value": "keep"}}}
	if e := state.WriteVersioned(filepath.Join(home, "credentials.json"), 1, doc, 0600); e != nil {
		t.Fatal(e)
	}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			calls++
			if e := r.ParseForm(); e != nil || r.Form.Get("refresh_token") != "refresh" {
				t.Error("refresh form")
			}
			fmt.Fprint(w, `{"access_token":"new","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" || r.Header.Get("chatgpt-account-id") != "account" {
			t.Error(r.Header)
		}
		var body obj
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["tool_choice"] != "required" || record(body["reasoning"])["effort"] != "high" {
			t.Error(body)
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"fallback\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"Cited answer\",\"annotations\":[{\"type\":\"url_citation\",\"url\":\"https://example.com/x?utm_source=openai\",\"title\":\"Citation\"}]}]}]}}\n\n")
	}))
	defer server.Close()
	t.Setenv("KI_CODEX_AUTH_BASE_URL", server.URL)
	t.Setenv("KI_DEEP_WEB_SEARCH_CODEX_URL", server.URL+"/responses")
	result, e := searchCodex(t.Context(), "query", obj{"numResults": 5}, obj{"codexModel": "model", "codexThinkingEffort": "high"})
	if e != nil || result["answer"] != "Cited answer" || record(array(result["results"])[0])["url"] != "https://example.com/x" {
		t.Fatal(result, e)
	}
	if _, e = getCredential(t.Context()); e != nil || calls != 1 {
		t.Fatal(calls, e)
	}
	saved, e := readCredential()
	if e != nil || record(record(saved.Document["providers"])["other"])["value"] != "keep" {
		t.Fatal(saved, e)
	}
}

func TestJavaScriptNormalizationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int
	}{
		{"0x10", 16}, {"0b11", 3}, {"0o17", 15}, {[]any{9}, 9}, {[]any{}, 1}, {[]any{nil}, 1},
		{[]any{1, 2}, 5}, {obj{}, 5}, {true, 1}, {nil, 1}, {"\uFEFF17\uFEFF", 17}, {"\u008517", 5},
		{"1e300", 20}, {"Infinity", 5}, {"NaN", 5}, {"-0x2", 5}, {"0x_2", 5},
	} {
		if got := clamp(tc.value, 1, 20, 5); got != tc.want {
			t.Errorf("clamp(%#v) = %d; want %d", tc.value, got, tc.want)
		}
	}
	if got := compact("\uFEFFalpha\u00A0beta\u0085gamma\uFEFF", 100); got != "alpha beta\u0085gamma" {
		t.Fatal(got)
	}
	cfg := normalizeConfig(obj{"provider": "codex ", "exaMode": "api ", "workflow": "none "})
	if cfg["provider"] != "all" || cfg["exaMode"] != "auto" || cfg["workflow"] != "none" {
		t.Fatal(cfg)
	}
	options, _ := normalizeOptions(obj{"provider": "exa ", "recencyFilter": "day "}, normalizeConfig(obj{"provider": "codex"}))
	if options["provider"] != "codex" || options["recencyFilter"] != nil {
		t.Fatal(options)
	}
}

func TestSourceURLQueryOrderAndCodexCitationText(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://example.com/x?z=a%20b&a=~&a=2", "https://example.com/x?z=a%20b&a=~&a=2"},
		{"https://example.com/x?z=a%20b&utm_source=x&a=~&a=2&flag&star=*", "https://example.com/x?z=a+b&a=%7E&a=2&flag=&star=*"},
		{"https://[2001:db8::1]:443/x/", "https://[2001:db8::1]/x"},
	} {
		if got := canonical(tc.raw); got != tc.want {
			t.Errorf("canonical(%s) = %s; want %s", tc.raw, got, tc.want)
		}
	}
	if got := cleanCodexURL("https://example.com/x?z=1&utm_source=openai&a=2&utm_source=other"); got != "https://example.com/x?z=1&a=2" {
		t.Fatal(got)
	}
	if got := cleanCodexURL("https://example.com/x?utm_source=other&utm_source=openai"); got != "https://example.com/x?utm_source=other&utm_source=openai" {
		t.Fatal(got)
	}
	part := obj{"text": "Read [the source](https://example.com/x).", "annotations": []any{obj{"type": "url_citation", "url": "https://example.com/x"}}}
	output := []any{obj{"type": "message", "content": []any{part}}}
	if got := record(sourcesFromOutput(output)[0])["snippet"]; got != "Read [the source](https://example.com/x)." {
		t.Fatal(got)
	}
	record(array(part["annotations"])[0])["start_index"] = float64(5)
	record(array(part["annotations"])[0])["end_index"] = float64(40)
	if got := record(sourcesFromOutput(output)[0])["snippet"]; got != "Read the source." {
		t.Fatal(got)
	}
}

func TestContentSlicesPreserveJavaScriptEndpoints(t *testing.T) {
	result := obj{"results": []any{obj{"title": "Title", "url": "https://example.com/x", "content": "abcdefghij"}}}
	if got := contentFromResult(result, obj{"offset": 1.8, "limit": 2.8}); got != "Title\nhttps://example.com/x\nbcd" {
		t.Fatal(got)
	}
	if got := contentFromResult(result, obj{"limit": nil}); got != "Title\nhttps://example.com/x\na" {
		t.Fatal(got)
	}
	if got := contentFromResult(result, obj{"urls": []any{" https://example.com/x "}}); got != "" {
		t.Fatal(got)
	}
	a := testApp(t)
	a.cache.put("response:id", result)
	out := a.getContent(obj{"responseId": "id", "offset": "0", "limit": "0"})
	if record(out["details"])["offset"] != "0" || record(out["details"])["limit"] != "0" || outputText(out) != "Title\nhttps://example.com/x\na" {
		t.Fatal(out)
	}
	if out := a.getContent(obj{"responseId": " id "}); out["isError"] != true {
		t.Fatal(out)
	}
}

func TestHTMLUsesOriginalEntityAndWhitespaceRules(t *testing.T) {
	if got := decodeEntities("&copy; &#128; &#0; &#x1F600; &#123abc; &nbsp;"); got != "&copy; \u0080 \x00 😀 {  " {
		t.Fatalf("%q", got)
	}
	title, body := htmlToText(`<title>&lt;b&gt;Title&lt;/b&gt; &copy;</title><p>First</p>` + "\u00A0\uFEFF" + `<p>Second &copy; &#128;</p>`)
	if title != "Title &copy;" || !strings.Contains(body, "First\nSecond &copy; \u0080") {
		t.Fatalf("%q %q", title, body)
	}
	if got := stripTags("<b>One</b>\uFEFFtwo\u0085three &copy;"); got != "One two\u0085three &copy;" {
		t.Fatal(got)
	}
	if _, e := safeFetchURL("https://@example.com/"); e != nil {
		t.Fatal(e)
	}
}

func TestCacheEvictsInInsertionOrderAcrossExpiryAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := &searchCache{path: path}
	c.put("expired", obj{"v": 1})
	c.put("kept", obj{"v": 2})
	c.mu.Lock()
	entry := c.entries["expired"]
	entry.CreatedAt = time.Now().Add(-time.Hour).UnixMilli()
	c.entries["expired"] = entry
	c.mu.Unlock()
	if c.get("expired", time.Minute) != nil {
		t.Fatal("not expired")
	}
	c.put("expired", obj{"v": 3})
	c.put("kept", obj{"v": 4}) // Updating an entry does not move it to the back.
	c = &searchCache{path: path}
	c.mu.Lock()
	c.load()
	order := append([]string(nil), c.order...)
	c.mu.Unlock()
	if !reflect.DeepEqual(order, []string{"kept", "expired"}) {
		t.Fatal(order)
	}
	for i := 0; i < 119; i++ {
		c.put(fmt.Sprintf("new-%d", i), obj{"v": i})
	}
	if c.get("kept", time.Minute) != nil || c.get("expired", time.Minute) == nil || len(c.entries) != 120 {
		t.Fatal(c.order, len(c.entries))
	}
}

func TestProviderOptionalFieldsAndExaHighlights(t *testing.T) {
	results, inline := mapExa([]any{obj{"url": "https://example.com/x", "title": " title ", "highlights": []any{" ", "", false}, "text": "fallback", "publishedDate": false}}, true)
	if record(results[0])["snippet"] != "" || record(results[0])["publishedAt"] != nil || record(inline[0])["content"] != "  " || record(inline[0])["title"] != " title " {
		t.Fatal(results, inline)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			fmt.Fprint(w, `{"result":{"content":[{"type":"text","text":17},{"type":"text","text":"valid text"}]}}`)
			return
		}
		if r.URL.Path == "/fetch" {
			fmt.Fprint(w, `{"results":[{"url":" https://example.com/x ","title":" title ","text":" body "}]}`)
			return
		}
		fmt.Fprint(w, `{"results":[{"url":"https://example.com/x","date":false}]}`)
	}))
	defer server.Close()
	t.Setenv("KI_EXA_MCP_URL", server.URL+"/mcp")
	t.Setenv("KI_TINYFISH_FETCH_URL", server.URL+"/fetch")
	t.Setenv("KI_TINYFISH_SEARCH_URL", server.URL+"/search")
	if got, e := exaMCPCall(t.Context(), "web_search_exa", obj{}); e != nil || got != "valid text" {
		t.Fatal(got, e)
	}
	fetched, e := fetchTinyfish(t.Context(), []string{"https://example.com/x"}, obj{"tinyfishApiKey": "key"})
	if e != nil || record(fetched[0])["url"] != " https://example.com/x " || record(fetched[0])["title"] != " title " {
		t.Fatal(fetched, e)
	}
	searched, e := searchTinyfish(t.Context(), "query", obj{"numResults": 5}, obj{"tinyfishApiKey": "key"})
	if e != nil || record(array(searched["results"])[0])["publishedAt"] != nil {
		t.Fatal(searched, e)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBackgroundHydrationTracksActiveCancellationButSurvivesReply(t *testing.T) {
	for _, activeCancel := range []bool{false, true} {
		t.Run(fmt.Sprint(activeCancel), func(t *testing.T) {
			sourceStarted, summaryStarted, sourceCanceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			original := httpClient
			httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Hostname() == "summary.example.com" {
					close(summaryStarted)
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				close(sourceStarted)
				select {
				case <-r.Context().Done():
					close(sourceCanceled)
					return nil, r.Context().Err()
				case <-release:
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("hydrated evidence")), Request: r}, nil
			})}
			defer func() { httpClient = original }()
			a := testApp(t)
			a.config = normalizeConfig(obj{"provider": "exa", "fetchContent": true})
			if activeCancel {
				a.config["workflow"] = "auto-summary"
				a.config["summaryModel"] = "openai/model"
			}
			t.Setenv("OPENAI_API_KEY", "key")
			t.Setenv("KI_DEEP_WEB_SEARCH_OPENAI_URL", "https://summary.example.com/responses")
			a.search = func(context.Context, string, string, obj, obj) (obj, error) {
				return obj{"provider": "exa", "results": []any{obj{"title": "source", "url": "https://source.example.com/x", "snippet": "evidence"}}}, nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			completed := make(chan any, 1)
			go func() {
				out, e := a.handle(ctx, "tool.execute", json.RawMessage(`{"name":"deep_web_search","args":{"query":"q"}}`))
				if e != nil {
					completed <- e
				} else {
					completed <- out
				}
			}()
			select {
			case <-sourceStarted:
			case <-time.After(time.Second):
				t.Fatal("hydration did not start")
			}
			if activeCancel {
				select {
				case <-summaryStarted:
				case <-time.After(time.Second):
					t.Fatal("summary did not start")
				}
				cancel()
				select {
				case <-sourceCanceled:
				case <-time.After(time.Second):
					t.Fatal("active cancellation did not reach hydration")
				}
				select {
				case <-completed:
				case <-time.After(time.Second):
					t.Fatal("cancelled tool did not finish")
				}
			} else {
				var out obj
				select {
				case value := <-completed:
					out = value.(obj)
				case <-time.After(time.Second):
					t.Fatal("source pack did not return")
				}
				cancel()
				select {
				case <-sourceCanceled:
					t.Fatal("successful reply cancelled hydration")
				case <-time.After(10 * time.Millisecond):
				}
				close(release)
				a.bg.Wait()
				cached := a.cache.get("response:"+str(record(out["details"])["responseId"]), time.Minute)
				if record(array(cached["results"])[0])["content"] != "hydrated evidence" {
					t.Fatal(cached)
				}
			}
			a.bg.Wait()
		})
	}
}

func TestWHATWGCanonicalURLsMatchOriginalConstructor(t *testing.T) {
	// Expected strings come from the original JavaScript URL constructor. These
	// cases affect both source deduplication and hosts admitted by content fetches.
	for _, tc := range []struct{ raw, want string }{
		{"https://例え.テスト/a/../b/", "https://xn--r8jz45g.xn--zckzah/b"},
		{"HTTPS://BÜCHER.Example:443/a/%2e/%2E%2e/b/", "https://xn--bcher-kva.example/b"},
		{"https://example.com/a//b/../c/", "https://example.com/a//c"},
		{`https:\\example.com\a\..\b?z=a%20b`, "https://example.com/b?z=a%20b"},
		{"http://127.1/secret", "http://127.0.0.1/secret"},
		{"http://2130706433/secret", "http://127.0.0.1/secret"},
		{"http://0x7f000001/secret", "http://127.0.0.1/secret"},
		{"http://0177.0.0.1/secret", "http://127.0.0.1/secret"},
		{"http://192.168.1/secret", "http://192.168.0.1/secret"},
		{"https://example.com:99999/x", ""},
		{"\uFEFFhttps://example.com/x", ""},
		{"https://example.com/a%2Fb", "https://example.com/a%2Fb"},
		{"https://example.com/x?z=a%20b&a=~", "https://example.com/x?z=a%20b&a=~"},
		{"https://@example.com/", "https://example.com/"},
		{"https://example.com/x?utm_source=x", "https://example.com/x"},
	} {
		if got := canonical(tc.raw); got != tc.want {
			t.Errorf("canonical(%q) = %q; want %q", tc.raw, got, tc.want)
		}
	}
}
func TestWHATWGContentSafetyAndRedirects(t *testing.T) {
	for _, raw := range []string{
		"http://127.1/secret", "http://2130706433/secret", "http://0x7f000001/secret", "http://0177.0.0.1/secret", "http://192.168.1/secret",
		`http:\\127.1\secret`, "http://１２７.１/secret", "http://%6cocalhost/secret",
	} {
		if _, e := safeFetchURL(raw); e == nil || !strings.Contains(e.Error(), "blocked") {
			t.Errorf("safeFetchURL(%q) = %v", raw, e)
		}
	}
	if u, e := safeFetchURL("https://BÜCHER.example/a/%2e%2e/b/"); e != nil || u.String() != "https://xn--bcher-kva.example/b/" {
		t.Fatal(u, e)
	}
	original := httpClient
	calls := 0
	httpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{`http:\\127.1\private`}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	defer func() { httpClient = original }()
	got := fetchOneContent(t.Context(), "https://public.example/path")
	if calls != 1 || !strings.Contains(str(got["error"]), "blocked") {
		t.Fatal(calls, got)
	}
}
