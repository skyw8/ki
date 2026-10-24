package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"ki/internal/state"
)

const deepseekQueryLimit = 128

func deepseekBaseURL() string {
	return strings.TrimRight(envOr("KI_DEEPSEEK_BASE_URL", envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com")), "/")
}

func deepseekAPIKey(config obj) string {
	if key := apiKey(config, "deepseekApiKey", "DEEPSEEK_API_KEY"); key != "" {
		return key
	}
	home := os.Getenv("KI_HOME")
	if home == "" {
		return ""
	}
	raw, _, e := state.ReadFile(filepath.Join(home, "credentials.json"), 1, nil)
	if e != nil {
		return ""
	}
	var document obj
	if json.Unmarshal(raw, &document) != nil {
		return ""
	}
	return str(record(record(document["providers"])["deepseek"])["apiKey"])
}

func deepseekClamp(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

func deepseekDateFilter(recency string) string {
	now := time.Now().UTC()
	switch recency {
	case "day":
		now = now.AddDate(0, 0, -1)
	case "week":
		now = now.AddDate(0, 0, -7)
	case "month":
		now = now.AddDate(0, -1, 0)
	case "year":
		now = now.AddDate(-1, 0, 0)
	default:
		return ""
	}
	return "after:" + now.Format("2006-01-02")
}

func deepseekQuery(query string, options obj) (string, string) {
	original := deepseekClamp(jsTrim(query), deepseekQueryLimit)
	parts := []string{}
	include, exclude := domains(options["domainFilter"])
	if len(include) == 1 {
		parts = append(parts, "site:"+include[0])
	} else if len(include) > 1 {
		clauses := make([]string, len(include))
		for i, domain := range include {
			clauses[i] = "site:" + domain
		}
		parts = append(parts, strings.Join(clauses, " OR "))
	}
	for _, domain := range exclude {
		parts = append(parts, "-site:"+domain)
	}
	if filter := deepseekDateFilter(str(options["recencyFilter"])); filter != "" {
		parts = append(parts, filter)
	}
	effective := original
	for _, part := range parts {
		candidate := effective + " " + part
		if utf8.RuneCountInString(candidate) > deepseekQueryLimit {
			continue
		}
		effective = candidate
	}
	return effective, original
}

func mapDeepseek(items any, inline bool) (results, contents []any) {
	results, contents = []any{}, []any{}
	seen := map[string]bool{}
	for _, v := range array(items) {
		item := record(v)
		u := str(item["url"])
		if u == "" || seen[rawStr(item["url"])] {
			continue
		}
		seen[rawStr(item["url"])] = true
		title := str(item["title"])
		if title == "" {
			title = u
		}
		snippet := compact(item["summary"], 800)
		if snippet == "" {
			snippet = compact(item["snippet"], 600)
		}
		out := obj{"url": u, "title": title, "snippet": snippet, "provider": "deepseek"}
		if truthy(item["date"]) {
			out["publishedAt"] = item["date"]
		}
		content := rawStr(item["content"])
		results = append(results, out)
		if inline && content != "" {
			contents = append(contents, obj{"url": u, "title": title, "content": content, "provider": "deepseek"})
		}
	}
	return
}

func searchDeepseek(ctx context.Context, query string, options, config obj) (obj, error) {
	key := deepseekAPIKey(config)
	if key == "" {
		return nil, fmt.Errorf("deepseek-auth-missing: deepseekApiKey, DEEPSEEK_API_KEY, or the Ki DeepSeek credential is required")
	}
	effective, original := deepseekQuery(query, options)
	body := obj{
		"query":        effective,
		"count":        clamp(options["numResults"], 1, 20, 5),
		"need_content": options["includeContent"] == true,
	}
	if effective != original {
		body["ori_question"] = original
	}
	data, e := jsonRequest(ctx, "POST", deepseekBaseURL()+"/v1/tools/search", map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"}, body, providerSearchBudget, "deepseek-search")
	if e != nil {
		return nil, e
	}
	values, ok := data["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("deepseek-search-invalid-response: results is not an array")
	}
	results, inline := mapDeepseek(values, options["includeContent"] == true)
	return obj{"answer": sourceAnswer(results), "results": results, "inlineContent": inline, "provider": "deepseek", "transport": "api"}, nil
}

func fetchDeepseek(ctx context.Context, rawURL string, config obj) (obj, error) {
	key := deepseekAPIKey(config)
	if key == "" {
		return nil, fmt.Errorf("deepseek-auth-missing: deepseekApiKey, DEEPSEEK_API_KEY, or the Ki DeepSeek credential is required")
	}
	u, e := safeFetchURL(rawURL)
	if e != nil {
		return nil, e
	}
	data, e := jsonRequest(ctx, "POST", deepseekBaseURL()+"/v1/tools/fetch", map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"}, obj{"url": u.String()}, contentBudget, "deepseek-fetch")
	if e != nil {
		return nil, e
	}
	content := rawStr(data["content"])
	if content == "" {
		return nil, fmt.Errorf("deepseek-fetch-empty: response contained no readable content")
	}
	return obj{"url": u.String(), "title": rawStr(data["title"]), "content": content, "provider": "deepseek", "fetchedBy": "deepseek"}, nil
}
