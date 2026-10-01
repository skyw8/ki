package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

func envOr(name, fallback string) string {
	if s := os.Getenv(name); s != "" {
		return s
	}
	return fallback
}
func providerAvailable(name string, config obj) bool {
	switch name {
	case "exa", "duckduckgo":
		return true
	case "tinyfish":
		return apiKey(config, "tinyfishApiKey", "TINYFISH_API_KEY") != ""
	case "codex":
		v, e := readCredential()
		return e == nil && str(v.Value["access"]) != "" && credentialAccount(v.Value) != ""
	}
	return false
}
func sourceAnswer(items []any) string {
	lines := []string{}
	for _, v := range items {
		item := record(v)
		lines = append(lines, fmt.Sprintf("%s\nSource: %s (%s)", str(item["snippet"]), str(item["title"]), str(item["url"])))
	}
	return strings.Join(lines, "\n\n")
}
func searchProvider(ctx context.Context, name, query string, options, config obj) (obj, error) {
	switch name {
	case "codex":
		return searchCodex(ctx, query, options, config)
	case "exa":
		return searchExa(ctx, query, options, config)
	case "tinyfish":
		return searchTinyfish(ctx, query, options, config)
	case "duckduckgo":
		return searchDuckduckgo(ctx, query, options)
	}
	return nil, fmt.Errorf("unknown provider %s", name)
}
func searchTinyfish(ctx context.Context, query string, options, config obj) (obj, error) {
	key := apiKey(config, "tinyfishApiKey", "TINYFISH_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("tinyfish-auth-missing: tinyfishApiKey is not configured")
	}
	u, _ := url.Parse(envOr("KI_TINYFISH_SEARCH_URL", "https://api.search.tinyfish.ai"))
	q := u.Query()
	q.Set("query", query)
	include, exclude := domains(options["domainFilter"])
	if len(include) > 0 {
		q.Set("include_domains", strings.Join(include, ","))
	}
	if len(exclude) > 0 {
		q.Set("exclude_domains", strings.Join(exclude, ","))
	}
	if recency := map[string]int{"day": 1440, "week": 10080, "month": 43200, "year": 525600}[str(options["recencyFilter"])]; recency > 0 {
		q.Set("recency_minutes", fmt.Sprint(recency))
	}
	u.RawQuery = q.Encode()
	data, e := jsonRequest(ctx, "GET", u.String(), map[string]string{"X-API-Key": key}, nil, providerSearchBudget, "tinyfish-search")
	if e != nil {
		return nil, e
	}
	values, ok := data["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("tinyfish-search-invalid-response: results is not an array")
	}
	items := []any{}
	for _, v := range values {
		item := record(v)
		s := str(item["url"])
		if s == "" {
			continue
		}
		title := str(item["title"])
		if title == "" {
			title = rawStr(item["url"])
		}
		out := obj{"url": s, "title": title, "snippet": compact(item["snippet"], 600), "provider": "tinyfish"}
		if truthy(item["date"]) {
			out["publishedAt"] = item["date"]
		}
		items = append(items, out)
		if len(items) >= clamp(options["numResults"], 1, 20, 5) {
			break
		}
	}
	return obj{"answer": sourceAnswer(items), "results": items, "inlineContent": []any{}, "provider": "tinyfish"}, nil
}
func fetchTinyfish(ctx context.Context, urls []string, config obj) ([]any, error) {
	key := apiKey(config, "tinyfishApiKey", "TINYFISH_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("tinyfish-auth-missing: tinyfishApiKey is not configured")
	}
	data, e := jsonRequest(ctx, "POST", envOr("KI_TINYFISH_FETCH_URL", "https://api.fetch.tinyfish.ai"), map[string]string{"X-API-Key": key, "Content-Type": "application/json"}, obj{"urls": urls, "format": "markdown", "per_url_timeout_ms": 30000}, contentBudget, "tinyfish-fetch")
	if e != nil {
		return nil, e
	}
	values, ok := data["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("tinyfish-fetch-invalid-response: results is not an array")
	}
	out := []any{}
	for _, v := range values {
		item := record(v)
		content := str(item["text"])
		if _, isString := item["text"].(string); !isString && truthy(item["text"]) {
			b, _ := json.Marshal(item["text"])
			content = string(b)
		}
		if content == "" {
			continue
		}
		s := rawStr(item["url"])
		if !truthy(item["url"]) {
			s = rawStr(item["final_url"])
		}
		out = append(out, obj{"url": s, "title": rawStr(item["title"]), "content": content, "provider": "tinyfish"})
	}
	return out, nil
}
func domainMatches(s string, options obj) bool {
	host := hostOf(s)
	include, exclude := domains(options["domainFilter"])
	matches := func(ds []string) bool {
		for _, d := range ds {
			if host == d || strings.HasSuffix(host, "."+d) {
				return true
			}
		}
		return false
	}
	return (len(include) == 0 || matches(include)) && !matches(exclude)
}

var ddgStart = regexp.MustCompile(`(?is)<div[^>]+class=["'][^"']*result[^"']*["'][^>]*>`)
var ddgAnchor = regexp.MustCompile(`(?is)<a[^>]+class=["'][^"']*result__a[^"']*["'][^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
var ddgSnippet = regexp.MustCompile(`(?is)class=["'][^"']*result__snippet[^"']*["'][^>]*>(.*?)</`)

func parseDuckduckgo(raw, base string, options obj) []any {
	starts := ddgStart.FindAllStringIndex(raw, -1)
	out := []any{}
	for i, pos := range starts {
		end := len(raw)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := raw[pos[1]:end]
		if strings.Contains(strings.ToLower(block), "result--ad") {
			continue
		}
		anchor := ddgAnchor.FindStringSubmatch(block)
		if len(anchor) == 0 {
			continue
		}
		u, e := url.Parse(base)
		if e != nil {
			continue
		}
		u, e = u.Parse(decodeEntities(anchor[1]))
		if e != nil {
			continue
		}
		if target := u.Query().Get("uddg"); target != "" {
			u, e = url.Parse(target)
			if e != nil {
				continue
			}
		}
		if !oneOf(u.Scheme, "http", "https") || !domainMatches(u.String(), options) {
			continue
		}
		title := stripTags(anchor[2])
		if title == "" {
			title = u.String()
		}
		snippet := ""
		if m := ddgSnippet.FindStringSubmatch(block); len(m) > 0 {
			snippet = stripTags(m[1])
		}
		out = append(out, obj{"title": title, "url": u.String(), "snippet": compact(snippet, 600), "provider": "duckduckgo"})
		if len(out) >= clamp(options["numResults"], 1, 20, 5) {
			break
		}
	}
	return out
}
func searchDuckduckgo(ctx context.Context, query string, options obj) (obj, error) {
	endpoint := envOr("KI_DUCKDUCKGO_URL", "https://html.duckduckgo.com/html/")
	u, e := url.Parse(endpoint)
	if e != nil {
		return nil, e
	}
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()
	raw, status, _, e := request(ctx, "GET", u.String(), map[string]string{"Accept": "text/html", "User-Agent": "Mozilla/5.0 (compatible; ki-deep-web-search/0.1)"}, nil, providerSearchBudget)
	if e != nil {
		return nil, e
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("duckduckgo-http-%d: %s", status, compact(string(raw), 260))
	}
	items := parseDuckduckgo(string(raw), endpoint, options)
	if len(items) == 0 {
		return nil, fmt.Errorf("duckduckgo-empty: response contained no parseable results")
	}
	return obj{"answer": sourceAnswer(items), "results": items, "provider": "duckduckgo"}, nil
}
func exaArgs(query string, options obj) obj {
	out := obj{"query": query, "type": "auto", "numResults": options["numResults"]}
	include, exclude := domains(options["domainFilter"])
	if len(include) > 0 {
		out["includeDomains"] = include
	}
	if len(exclude) > 0 {
		out["excludeDomains"] = exclude
	}
	if days := map[string]int{"day": 1, "week": 7, "month": 30, "year": 365}[str(options["recencyFilter"])]; days > 0 {
		out["startPublishedDate"] = time.Now().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if options["includeContent"] == true {
		out["contents"] = obj{"text": obj{"maxCharacters": 4000}, "highlights": obj{"maxCharacters": 1500}}
	}
	return out
}
func mapExa(items any, inline bool) (results, contents []any) {
	results, contents = []any{}, []any{}
	seen := map[string]bool{}
	for _, v := range array(items) {
		item := record(v)
		s := str(item["url"])
		if s == "" || seen[rawStr(item["url"])] {
			continue
		}
		seen[rawStr(item["url"])] = true
		title := str(item["title"])
		if title == "" {
			title = rawStr(item["url"])
		}
		highlights := []string{}
		for _, v := range array(item["highlights"]) {
			if s, ok := v.(string); ok {
				highlights = append(highlights, s)
			}
		}
		content := strings.Join(highlights, " ")
		if content == "" {
			content = rawStr(item["text"])
		}
		out := obj{"url": rawStr(item["url"]), "title": title, "snippet": compact(content, 600), "provider": "exa"}
		if truthy(item["publishedDate"]) {
			out["publishedAt"] = item["publishedDate"]
		}
		results = append(results, out)
		if inline && content != "" {
			contents = append(contents, obj{"url": rawStr(item["url"]), "title": rawStr(item["title"]), "content": content, "provider": "exa"})
		}
	}
	return
}
func exaMCPQuery(query string, options obj) string {
	parts := []string{query}
	for _, d := range stringList(options["domainFilter"]) {
		if strings.HasPrefix(d, "-") {
			parts = append(parts, "-site:"+d[1:])
		} else {
			parts = append(parts, "site:"+d)
		}
	}
	if recency := map[string]string{"day": "past 24 hours", "week": "past week", "month": "past month", "year": "past year"}[str(options["recencyFilter"])]; recency != "" {
		parts = append(parts, recency)
	}
	return strings.Join(parts, " ")
}
func parseMCPText(s string) []any {
	var data obj
	if json.Unmarshal([]byte(s), &data) == nil {
		if values, ok := data["results"].([]any); ok {
			return values
		}
	}
	starts := regexp.MustCompile(`(?m)^Title: `).FindAllStringIndex(s, -1)
	out := []any{}
	for i, start := range starts {
		end := len(s)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := s[start[0]:end]
		get := func(key string) string {
			m := regexp.MustCompile(`(?m)^` + key + `:\s*(.+)$`).FindStringSubmatch(block)
			if len(m) > 0 {
				return strings.TrimSpace(m[1])
			}
			return ""
		}
		u := get("URL")
		if u == "" {
			continue
		}
		content := ""
		if at := strings.Index(block, "\nText: "); at >= 0 {
			content = block[at+7:]
		} else if at := strings.Index(block, "\nHighlights:"); at >= 0 {
			content = block[at+12:]
		}
		content = strings.TrimSpace(regexp.MustCompile(`\n---\s*$`).ReplaceAllString(content, ""))
		out = append(out, obj{"title": get("Title"), "url": u, "text": content})
	}
	return out
}
func exaMCPCall(ctx context.Context, tool string, args obj) (string, error) {
	endpoint := envOr("KI_EXA_MCP_URL", "https://mcp.exa.ai/mcp")
	u, e := url.Parse(endpoint)
	if e != nil {
		return "", e
	}
	q := u.Query()
	q.Set("tools", tool)
	u.RawQuery = q.Encode()
	raw, status, _, e := request(ctx, "POST", u.String(), map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "x-exa-source": "ki-deep-web-search"}, obj{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": obj{"name": tool, "arguments": args}}, providerSearchBudget)
	if e != nil {
		return "", e
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("exa-mcp-http-%d: %s", status, compact(string(raw), 260))
	}
	var rpc obj
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var candidate obj
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &candidate) == nil && (truthy(candidate["result"]) || truthy(candidate["error"])) {
			rpc = candidate
			break
		}
	}
	if rpc == nil && json.Unmarshal(raw, &rpc) != nil {
		return "", fmt.Errorf("exa-mcp-invalid-response: response was not JSON-RPC")
	}
	if truthy(rpc["error"]) {
		message := str(record(rpc["error"])["message"])
		if message == "" {
			message = "MCP call failed"
		}
		return "", fmt.Errorf("exa-mcp-error: %s", message)
	}
	result := record(rpc["result"])
	content := ""
	for _, v := range array(result["content"]) {
		item := record(v)
		if item["type"] == "text" {
			if _, ok := item["text"].(string); !ok && !truthy(result["isError"]) {
				continue
			}
			content = rawStr(item["text"])
			break
		}
	}
	if truthy(result["isError"]) {
		return "", fmt.Errorf("exa-mcp-error: %s", compact(content, 260))
	}
	if content == "" {
		return "", fmt.Errorf("exa-mcp-empty: MCP returned no content")
	}
	return content, nil
}
func searchExa(ctx context.Context, query string, options, config obj) (obj, error) {
	key := apiKey(config, "exaApiKey", "EXA_API_KEY")
	mode := str(config["exaMode"])
	if mode == "api" && key == "" {
		return nil, fmt.Errorf("exa-auth-missing: exaApiKey is required when exaMode=api")
	}
	if mode != "mcp" && key != "" {
		useAnswer := options["includeContent"] != true && str(options["recencyFilter"]) == "" && len(array(options["domainFilter"])) == 0 && clamp(options["numResults"], 1, 20, 5) == 5
		body := exaArgs(query, options)
		route := "search"
		if useAnswer {
			body = obj{"query": query}
			route = "answer"
		} else {
			contents := obj{"highlights": true}
			if options["includeContent"] == true {
				contents["text"] = true
			}
			body["contents"] = contents
		}
		data, e := jsonRequest(ctx, "POST", strings.TrimRight(envOr("EXA_BASE_URL", "https://api.exa.ai"), "/")+"/"+route, map[string]string{"x-api-key": key, "Content-Type": "application/json", "x-exa-integration": "ki-deep-web-search"}, body, providerSearchBudget, "exa-api")
		if e != nil {
			return nil, e
		}
		items := data["results"]
		if useAnswer {
			items = data["citations"]
		}
		results, inline := mapExa(items, options["includeContent"] == true)
		answer := sourceAnswer(results)
		if useAnswer {
			answer = compact(data["answer"], 5000)
		}
		return obj{"answer": answer, "results": results, "inlineContent": inline, "provider": "exa", "transport": "api"}, nil
	}
	filtered := options["includeContent"] == true || str(options["recencyFilter"]) != "" || len(array(options["domainFilter"])) > 0
	tool := "web_search_exa"
	args := obj{"query": exaMCPQuery(query, options), "numResults": options["numResults"]}
	if filtered {
		tool = "web_search_advanced_exa"
		args = exaArgs(query, options)
		args["enableHighlights"] = true
		args["textMaxCharacters"] = 3000
		if options["includeContent"] == true {
			args["textMaxCharacters"] = 50000
		}
	}
	raw, e := exaMCPCall(ctx, tool, args)
	if e != nil && filtered && ctx.Err() == nil && !strings.Contains(strings.ToLower(e.Error()), "abort") {
		raw, e = exaMCPCall(ctx, "web_search_exa", obj{"query": exaMCPQuery(query, options), "numResults": options["numResults"]})
	}
	if e != nil {
		return nil, e
	}
	results, inline := mapExa(parseMCPText(raw), options["includeContent"] == true)
	return obj{"answer": sourceAnswer(results), "results": results, "inlineContent": inline, "provider": "exa", "transport": "mcp"}, nil
}
