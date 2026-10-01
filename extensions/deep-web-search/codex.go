package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	whatwg "github.com/nlnwa/whatwg-url/url"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"ki/internal/state"
)

type credential struct {
	Path            string
	Document, Value obj
}

var refreshMu sync.Mutex

func accountFromAccess(s string) string {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return ""
	}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if e != nil {
		return ""
	}
	var value obj
	if json.Unmarshal(b, &value) != nil {
		return ""
	}
	return str(record(value["https://api.openai.com/auth"])["chatgpt_account_id"])
}
func credentialAccount(v obj) string {
	if s, ok := v["accountId"].(string); ok {
		return jsTrim(s)
	}
	return accountFromAccess(str(v["access"]))
}
func readCredential() (credential, error) {
	path := filepath.Join(os.Getenv("KI_HOME"), "credentials.json")
	raw, _, e := state.ReadFile(path, 1, nil)
	if os.IsNotExist(e) {
		return credential{}, fmt.Errorf("codex-auth-missing: KI credentials.json was not found")
	}
	if e != nil {
		return credential{}, fmt.Errorf("codex-auth-invalid: KI credentials.json could not be parsed: %w", e)
	}
	var document obj
	if json.Unmarshal(raw, &document) != nil {
		return credential{}, fmt.Errorf("codex-auth-invalid: KI credentials.json could not be parsed")
	}
	entry := record(record(document["providers"])["openai-codex"])
	value, ok := entry["value"].(map[string]any)
	if entry["type"] != "oauth" || !ok {
		return credential{}, fmt.Errorf("codex-auth-missing: openai-codex OAuth credential is not configured")
	}
	if str(value["access"]) == "" {
		return credential{}, fmt.Errorf("codex-auth-invalid: openai-codex access token is empty")
	}
	return credential{path, document, value}, nil
}
func credentialFresh(c credential) bool {
	return number(c.Value["expires"]) > float64(time.Now().UnixMilli()+60000)
}
func getCredential(ctx context.Context) (credential, error) {
	c, e := readCredential()
	if e != nil || credentialFresh(c) {
		return c, e
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	c, e = readCredential()
	if e != nil || credentialFresh(c) {
		return c, e
	}
	refresh := str(c.Value["refresh"])
	if refresh == "" {
		return c, fmt.Errorf("codex-auth-expired: OAuth credential has no refresh token")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"app_EMoamEEZ73f0CkXaXp7hrann"}}
	raw, status, _, e := request(ctx, "POST", strings.TrimRight(envOr("KI_CODEX_AUTH_BASE_URL", "https://auth.openai.com"), "/")+"/oauth/token", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, form.Encode(), 30*time.Second)
	if e != nil {
		return c, e
	}
	if status < 200 || status >= 300 {
		return c, fmt.Errorf("codex-auth-refresh-failed: OAuth token refresh returned HTTP %d", status)
	}
	token := obj{}
	_ = json.Unmarshal(raw, &token)
	access := str(token["access_token"])
	expiresIn := number(token["expires_in"])
	if access == "" || expiresIn <= 0 {
		return c, fmt.Errorf("codex-auth-refresh-failed: OAuth refresh response was incomplete")
	}
	next := clone(c.Value)
	next["access"] = access
	if s := str(token["refresh_token"]); s != "" {
		next["refresh"] = s
	}
	next["expires"] = time.Now().UnixMilli() + int64(expiresIn*1000)
	account := accountFromAccess(access)
	if account == "" {
		account = credentialAccount(c.Value)
	}
	next["accountId"] = account
	lockPath := c.Path + ".lock"
	var lock *os.File
	for attempt := 0; attempt < 100; attempt++ {
		lock, e = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			break
		}
		if !os.IsExist(e) {
			return c, e
		}
		select {
		case <-ctx.Done():
			return c, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if lock == nil {
		return c, fmt.Errorf("codex-auth-refresh-busy: credentials.json refresh lock is busy")
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()
	latest, e := readCredential()
	if e != nil {
		return c, e
	}
	if credentialFresh(latest) {
		return latest, nil
	}
	latest.Value = next
	record(record(latest.Document["providers"])["openai-codex"])["value"] = next
	latest.Document["version"] = 1
	// Read again under the file lock so refreshed credentials do not overwrite
	// unrelated provider changes made while the HTTP refresh was in flight.
	if e := state.WriteVersioned(c.Path, 1, latest.Document, 0600); e != nil {
		return c, e
	}
	return latest, nil
}
func sourceFilters(options obj) obj {
	include, exclude := domains(options["domainFilter"])
	out := obj{}
	if len(include) > 0 {
		out["allowed_domains"] = include
	}
	if len(exclude) > 0 {
		out["blocked_domains"] = exclude
	}
	return out
}
func searchInstructions(options obj) string {
	parts := []string{"Search the web and return a concise answer grounded only in the retrieved sources.", "Cite the source URLs in the answer when possible."}
	if label := map[string]string{"day": "past 24 hours", "week": "past week", "month": "past month", "year": "past year"}[str(options["recencyFilter"])]; label != "" {
		parts = append(parts, "Prefer sources from the "+label+".")
	}
	if n := number(options["numResults"]); n > 0 {
		parts = append(parts, fmt.Sprintf("Prefer around %.0f distinct sources.", n))
	}
	include, exclude := domains(options["domainFilter"])
	if len(include) > 0 {
		parts = append(parts, "Only use "+strings.Join(include, ", ")+".")
	}
	if len(exclude) > 0 {
		parts = append(parts, "Do not use "+strings.Join(exclude, ", ")+".")
	}
	return strings.Join(parts, " ")
}
func parseResponsesBody(raw string) (payload obj, streamedText string) {
	if trimmed := jsTrim(raw); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var value any
		if json.Unmarshal([]byte(trimmed), &value) == nil {
			if a, ok := value.([]any); ok {
				return obj{"output": a}, ""
			}
			return record(value), ""
		}
	}
	items := []any{}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event obj
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &event) != nil {
			continue
		}
		switch str(event["type"]) {
		case "response.output_text.delta":
			streamedText += rawStr(event["delta"])
		case "response.output_item.done":
			if event["item"] != nil {
				items = append(items, event["item"])
			}
		case "response.done", "response.completed":
			if response, ok := event["response"].(map[string]any); ok {
				payload = response
			}
		}
	}
	if payload == nil {
		payload = obj{}
	}
	if len(array(payload["output"])) == 0 {
		payload["output"] = items
	}
	return payload, streamedText
}
func answerFromOutput(output []any, streamed string) string {
	parts := []string{}
	for _, v := range output {
		item := record(v)
		if s, ok := item["output_text"].(string); ok {
			parts = append(parts, s)
		}
		if item["type"] != "message" {
			continue
		}
		for _, v := range array(item["content"]) {
			if s := rawStr(record(v)["text"]); jsTrim(s) != "" {
				parts = append(parts, s)
			}
		}
	}
	answer := strings.Join(parts, "\n")
	if answer == "" {
		answer = streamed
	}
	return jsTrim(answer)
}
func cleanCodexURL(s string) string {
	u, e := whatwg.Parse(s)
	if e != nil {
		return ""
	}
	if queryValue(u.Query(), "utm_source") == "openai" {
		return rewriteURLQuery(u.Href(true), u.Query(), func(key, value string) bool { return key == "utm_source" })
	}
	return u.Href(true)
}

func sourcesFromOutput(output []any) []any {
	out := []any{}
	seen := map[string]bool{}
	add := func(raw, title, snippet string) {
		u := cleanCodexURL(raw)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		if jsTrim(title) == "" {
			title = u
		}
		out = append(out, obj{"title": jsTrim(title), "url": u, "snippet": compact(snippet, 500), "provider": "codex"})
	}
	for _, v := range output {
		item := record(v)
		if item["type"] != "message" {
			continue
		}
		for _, v := range array(item["content"]) {
			part := record(v)
			for _, v := range array(part["annotations"]) {
				a := record(v)
				if a["type"] != "url_citation" {
					continue
				}
				snippet := rawStr(part["text"])
				start, end := number(a["start_index"]), number(a["end_index"])
				_, startOK := a["start_index"].(float64)
				_, endOK := a["end_index"].(float64)
				if startOK && endOK && !math.IsNaN(start) && !math.IsNaN(end) && !math.IsInf(start, 0) && !math.IsInf(end, 0) {
					snippet = sliceText(snippet, max(0, int(start)-110), min(len(utf16.Encode([]rune(snippet))), int(end)+110))
					snippet = regexpMarkdownLink.ReplaceAllString(snippet, "$1")
				}
				add(str(a["url"]), str(a["title"]), compact(snippet, 350))
			}
		}
	}
	for _, v := range output {
		item := record(v)
		if item["type"] != "web_search_call" {
			continue
		}
		for _, group := range []any{record(item["action"])["sources"], item["sources"], item["results"]} {
			for _, v := range array(group) {
				source := record(v)
				u := str(source["url"])
				if u == "" {
					u = str(source["source_website_url"])
				}
				title := str(source["title"])
				if title == "" {
					title = str(source["caption"])
				}
				snippet := str(source["text"])
				if snippet == "" {
					snippet = str(source["snippet"])
				}
				add(u, title, snippet)
			}
		}
	}
	return out
}
func responsesRequest(ctx context.Context, model, prompt, effort, provider string, search obj) (obj, string, error) {
	codex := provider != "openai"
	access, account := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), ""
	if codex {
		credential, e := getCredential(ctx)
		if e != nil {
			return nil, "", e
		}
		access = str(credential.Value["access"])
		account = credentialAccount(credential.Value)
	} else if access == "" {
		return nil, "", fmt.Errorf("openai-auth-missing: OPENAI_API_KEY is not configured")
	}
	endpoint := envOr("KI_DEEP_WEB_SEARCH_OPENAI_URL", "https://api.openai.com/v1/responses")
	headers := map[string]string{"Authorization": "Bearer " + access, "Content-Type": "application/json", "OpenAI-Beta": "responses=experimental"}
	if codex {
		endpoint = envOr("KI_DEEP_WEB_SEARCH_CODEX_URL", "https://chatgpt.com/backend-api/codex/responses")
		if account != "" {
			headers["chatgpt-account-id"] = account
			headers["originator"] = "pi"
		}
	}
	instructions := "Answer using only the supplied evidence. Do not invent citations or facts."
	if search != nil {
		instructions = searchInstructions(search)
	}
	body := obj{"model": model, "instructions": instructions, "input": []any{obj{"role": "user", "content": []any{obj{"type": "input_text", "text": prompt}}}}, "store": false, "stream": true}
	if effort = jsTrim(effort); effort != "" && effort != "off" {
		body["reasoning"] = obj{"effort": effort}
	}
	budget := 30 * time.Second
	if search != nil {
		budget = codexSearchBudget
		tool := obj{"type": "web_search"}
		if filters := sourceFilters(search); len(filters) > 0 {
			tool["filters"] = filters
		}
		body["tools"] = []any{tool}
		body["include"] = []string{"web_search_call.action.sources"}
		body["tool_choice"] = "required"
		body["parallel_tool_calls"] = true
	}
	raw, status, _, e := request(ctx, "POST", endpoint, headers, body, budget)
	if e != nil {
		return nil, "", fmt.Errorf("codex-network-error: %w", e)
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("codex-http-%d: %s", status, compact(string(raw), 260))
	}
	payload, streamed := parseResponsesBody(string(raw))
	if trimmed := strings.TrimSpace(string(raw)); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var verify any
		if e := json.Unmarshal(raw, &verify); e != nil {
			return nil, "", fmt.Errorf("codex-invalid-response: %w", e)
		}
	}
	return payload, streamed, nil
}
func searchCodex(ctx context.Context, query string, options, config obj) (obj, error) {
	model := str(config["codexModel"])
	if model == "" {
		model = "gpt-6-astra"
	}
	payload, streamed, e := responsesRequest(ctx, model, query, str(config["codexThinkingEffort"]), "openai-codex", options)
	if e != nil {
		return nil, e
	}
	output := array(payload["output"])
	results := sourcesFromOutput(output)
	results = results[:min(len(results), clamp(options["numResults"], 1, 20, 5))]
	answer := answerFromOutput(output, streamed)
	if answer == "" && len(results) == 0 {
		return nil, fmt.Errorf("codex-empty-search: Codex returned no answer or sources")
	}
	return obj{"answer": answer, "results": results, "provider": "codex", "model": model}, nil
}
func completeWithModel(ctx context.Context, prompt, model, effort string, deadline time.Duration) (string, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", fmt.Errorf("summary-model-missing: summaryModel is empty")
	}
	provider := "openai-codex"
	if ix := strings.Index(model, "/"); ix >= 0 {
		provider, model = model[:ix], model[ix+1:]
	}
	if model == "" {
		return "", "", fmt.Errorf("summary-model-missing: summaryModel is empty")
	}
	if !oneOf(provider, "openai-codex", "openai") {
		return "", "", fmt.Errorf("summary-model-unsupported: %s is not supported by the sidecar completion adapter", provider)
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	payload, streamed, e := responsesRequest(ctx, model, prompt, effort, provider, nil)
	if e != nil {
		return "", "", e
	}
	answer := answerFromOutput(array(payload["output"]), streamed)
	if answer == "" {
		return "", "", fmt.Errorf("summary-empty: model returned no text")
	}
	return answer, provider + "/" + model, nil
}
