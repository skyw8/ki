package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"ki/internal/state"
)

func TestDeepseekSearchFiltersAndContent(t *testing.T) {
	var searchBody obj
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/search" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if e := json.NewDecoder(r.Body).Decode(&searchBody); e != nil {
			t.Error(e)
		}
		fmt.Fprint(w, `{"results":[
			{"title":" One ","url":"https://example.com/a","snippet":"short","summary":"long summary","content":"body","date":"2026-10-18T00:00:00Z","authority":3},
			{"title":"","url":"https://example.com/b","snippet":"fallback","summary":"","content":"","date":null}
		]}`)
	}))
	defer server.Close()
	t.Setenv("KI_DEEPSEEK_BASE_URL", server.URL)
	options := obj{
		"numResults":     5,
		"includeContent": true,
		"domainFilter":   []any{"example.com", "-spam.example.com"},
		"recencyFilter":  "week",
	}
	result, e := searchDeepseek(t.Context(), "release", options, obj{"deepseekApiKey": "secret"})
	if e != nil {
		t.Fatal(e)
	}
	query := str(searchBody["query"])
	for _, part := range []string{"release", "site:example.com", "-site:spam.example.com", "after:"} {
		if !strings.Contains(query, part) {
			t.Fatalf("query %q is missing %q", query, part)
		}
	}
	if searchBody["ori_question"] != "release" || number(searchBody["count"]) != 5 || searchBody["need_content"] != true {
		t.Fatal(searchBody)
	}
	items := array(result["results"])
	if len(items) != 2 {
		t.Fatal(items)
	}
	first := record(items[0])
	if first["title"] != "One" || first["snippet"] != "long summary" || first["publishedAt"] != "2026-10-18T00:00:00Z" {
		t.Fatal(first)
	}
	second := record(items[1])
	if second["title"] != "https://example.com/b" || second["snippet"] != "fallback" {
		t.Fatal(second)
	}
	inline := array(result["inlineContent"])
	if len(inline) != 1 || record(inline[0])["content"] != "body" || result["transport"] != "api" {
		t.Fatal(result)
	}
}

func TestDeepseekFetch(t *testing.T) {
	var fetchBody obj
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/fetch" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if e := json.NewDecoder(r.Body).Decode(&fetchBody); e != nil {
			t.Error(e)
		}
		fmt.Fprint(w, `{"title":"Example article","content":"# Example article\n\nArticle content."}`)
	}))
	defer server.Close()
	t.Setenv("KI_DEEPSEEK_BASE_URL", server.URL)
	fetched, e := fetchDeepseek(t.Context(), "https://example.com/page", obj{"deepseekApiKey": "secret"})
	if e != nil || fetchBody["url"] != "https://example.com/page" {
		t.Fatal(fetched, fetchBody, e)
	}
	if fetched["title"] != "Example article" || !strings.Contains(str(fetched["content"]), "Article content.") || fetched["fetchedBy"] != "deepseek" {
		t.Fatal(fetched)
	}
	if _, e := fetchDeepseek(t.Context(), "file:///tmp/secret", obj{"deepseekApiKey": "secret"}); e == nil {
		t.Fatal("accepted a non-HTTP URL")
	}
	if _, e := fetchDeepseek(t.Context(), "https://example.com/page", obj{}); e == nil || !strings.Contains(e.Error(), "auth-missing") {
		t.Fatal(e)
	}
}

func TestDeepseekCredentialFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KI_HOME", home)
	t.Setenv("DEEPSEEK_API_KEY", "")
	if key := deepseekAPIKey(obj{}); key != "" {
		t.Fatal(key)
	}
	path := filepath.Join(home, "credentials.json")
	if e := state.WriteVersioned(path, 1, obj{"version": 1, "providers": obj{"deepseek": obj{"apiKey": "stored"}}}, 0600); e != nil {
		t.Fatal(e)
	}
	if key := deepseekAPIKey(obj{}); key != "stored" {
		t.Fatal(key)
	}
	t.Setenv("DEEPSEEK_API_KEY", "env")
	if key := deepseekAPIKey(obj{}); key != "env" {
		t.Fatal(key)
	}
	if key := deepseekAPIKey(obj{"deepseekApiKey": "explicit"}); key != "explicit" {
		t.Fatal(key)
	}
}

func TestDeepseekQueryRespectsAPILimit(t *testing.T) {
	query, original := deepseekQuery("release", obj{"domainFilter": []any{"example.com"}, "recencyFilter": "day"})
	if original != "release" || !strings.HasPrefix(query, "release site:example.com after:") {
		t.Fatal(query, original)
	}
	long := strings.Repeat("x", deepseekQueryLimit)
	query, original = deepseekQuery(long, obj{"domainFilter": []any{"example.com"}, "recencyFilter": "day"})
	if original != long || query != long || utf8.RuneCountInString(query) > deepseekQueryLimit {
		t.Fatal(len(query), len(original))
	}
}
