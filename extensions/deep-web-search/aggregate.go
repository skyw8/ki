package main

import (
	"context"
	"fmt"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

var regexpMarkdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

func mergeResults(runs []any, limit int) []any {
	merged := map[string]obj{}
	order := []string{}
	weights := map[string]float64{"codex": 1.15, "exa": 1.08, "tinyfish": 1, "duckduckgo": .88}
	for _, v := range runs {
		run := record(v)
		provider := str(run["provider"])
		weight := weights[provider]
		if weight == 0 {
			weight = 1
		}
		for index, v := range array(run["results"]) {
			raw := record(v)
			u := canonical(rawStr(raw["url"]))
			if u == "" {
				continue
			}
			existing := merged[u]
			if existing == nil {
				order = append(order, u)
			}
			candidate := clone(raw)
			candidate["url"] = u
			title := rawStr(raw["title"])
			if !truthy(raw["title"]) {
				title = u
			}
			candidate["title"] = compact(title, 240)
			candidate["snippet"] = compact(raw["snippet"], 800)
			names := stringList(existing["providers"])
			names = unique(append(names, provider))
			candidate["providers"] = names
			ranks := clone(record(existing["ranks"]))
			ranks[provider] = index + 1
			candidate["ranks"] = ranks
			score := number(existing["score"])
			if math.IsNaN(score) {
				score = 0
			}
			candidate["score"] = score + weight/float64(61+index)
			for _, field := range []string{"content", "contentError"} {
				value := rawStr(raw[field])
				if value == "" {
					value = rawStr(existing[field])
				}
				candidate[field] = value
			}
			merged[u] = candidate
		}
	}
	sorted := make([]obj, 0, len(merged))
	for _, u := range order {
		sorted = append(sorted, merged[u])
	}
	urlCollator := collate.New(language.English)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := number(sorted[i]["score"]), number(sorted[j]["score"])
		if left == right {
			return urlCollator.CompareString(str(sorted[i]["url"]), str(sorted[j]["url"])) < 0
		}
		return left > right
	})
	chosen := []any{}
	counts := map[string]int{}
	for _, item := range sorted {
		duplicate := false
		for _, v := range chosen {
			existing := record(v)
			sameHost := hostOf(str(existing["url"])) != "" && hostOf(str(existing["url"])) == hostOf(str(item["url"]))
			if sameHost && similarity(str(existing["title"]), str(item["title"])) >= .86 || similarity(str(existing["title"])+" "+str(existing["snippet"]), str(item["title"])+" "+str(item["snippet"])) >= .93 {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		domain := hostOf(str(item["url"]))
		cap := limit
		if len(runs) > 1 {
			cap = max(2, int(math.Ceil(float64(limit)/2)))
		}
		if counts[domain] >= cap {
			continue
		}
		counts[domain]++
		chosen = append(chosen, item)
		if len(chosen) >= limit {
			break
		}
	}
	return chosen
}
func providerError(name string, err error, duration int64) obj {
	message := err.Error()
	lower := strings.ToLower(message)
	status := regexp.MustCompile(`(?i)(?:http-|HTTP |status )([45]\d\d)`).FindStringSubmatch(message)
	category := "provider"
	if len(status) > 0 && status[1] == "401" {
		category = "auth"
	} else if len(status) > 0 && status[1] == "429" {
		category = "rate-limit"
	} else if strings.Contains(lower, "timeout") || strings.Contains(lower, "budget") || strings.Contains(lower, "abort") || strings.Contains(lower, "deadline") || strings.Contains(lower, "canceled") {
		category = "timeout"
	} else if strings.Contains(lower, "network") || strings.Contains(lower, "fetch") {
		category = "network"
	}
	return obj{"provider": name, "ok": false, "durationMs": duration, "error": compact(message, 320), "category": category}
}

type providerOutcome struct {
	Result   obj
	Err      error
	Duration int64
	Provider string
}
type providerSearch func(context.Context, string, string, obj, obj) (obj, error)

func aggregateSearch(ctx context.Context, query string, options, config obj, search providerSearch, progress func(obj)) (obj, error) {
	selected := enabledProviders(config, options["provider"])
	if len(selected) == 0 {
		return nil, fmt.Errorf("provider-config-error: all selected providers are disabled")
	}
	if str(options["proxy"]) != "" {
		return nil, fmt.Errorf("proxy-unsupported: this sidecar currently supports direct HTTP(S) only")
	}
	diagnostics := []any{}
	active := []string{}
	for _, provider := range selected {
		if !providerAvailable(provider, config) {
			message := "provider credentials are not configured"
			if provider == "codex" {
				message = "codex-auth-missing"
			}
			diagnostics = append(diagnostics, obj{"provider": provider, "ok": false, "category": "unavailable", "error": message})
		} else {
			active = append(active, provider)
		}
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("provider-config-error: no enabled provider has usable credentials")
	}
	toolCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, queryBudget)
	defer cancel()
	tasks := []func() (any, error){}
	for _, provider := range active {
		tasks = append(tasks, func() (any, error) {
			child, stop := context.WithTimeout(ctx, searchBudget(provider))
			defer stop()
			started := time.Now()
			progress(obj{"phase": "provider", "provider": provider, "status": "running"})
			result, e := search(child, provider, query, options, config)
			duration := time.Since(started).Milliseconds()
			p := obj{"phase": "provider", "provider": provider, "durationMs": duration}
			if e != nil {
				p["status"] = "failed"
				p["error"] = compact(e.Error(), 320)
			} else {
				p["status"] = "done"
				p["count"] = len(array(result["results"]))
			}
			progress(p)
			return providerOutcome{result, e, duration, provider}, nil
		})
	}
	quorum := min(2, len(tasks))
	for _, p := range active {
		if p == "codex" {
			quorum = len(tasks)
		}
	}
	settled := settleWithGrace(ctx, tasks, quorum, providerGrace, cancel)
	runs := []any{}
	for index, item := range settled {
		if !item.Present {
			diagnostics = append(diagnostics, obj{"provider": active[index], "ok": false, "category": "timeout", "error": "provider-budget-exceeded", "durationMs": searchBudget(active[index]).Milliseconds()})
			continue
		}
		value := item.Value.(providerOutcome)
		if value.Err != nil {
			diagnostics = append(diagnostics, providerError(value.Provider, value.Err, value.Duration))
			continue
		}
		run := clone(value.Result)
		run["provider"] = value.Provider
		run["durationMs"] = value.Duration
		runs = append(runs, run)
		d := obj{"provider": value.Provider, "ok": true, "durationMs": value.Duration, "count": len(array(run["results"]))}
		if run["transport"] != nil {
			d["transport"] = run["transport"]
		}
		diagnostics = append(diagnostics, d)
	}
	if len(runs) == 0 {
		messages := []string{}
		for _, v := range diagnostics {
			d := record(v)
			messages = append(messages, str(d["provider"])+": "+str(d["error"]))
		}
		return nil, fmt.Errorf("provider-failed: %s", strings.Join(messages, "; "))
	}
	results := mergeResults(runs, clamp(options["numResults"], 1, 20, 5))
	inlineByURL := map[string]obj{}
	answers := []string{}
	for _, v := range runs {
		run := record(v)
		if s := str(run["answer"]); s != "" {
			answers = append(answers, rawStr(run["answer"]))
		}
		for _, v := range array(run["inlineContent"]) {
			item := record(v)
			key := canonical(str(item["url"]))
			if key == "" {
				key = str(item["url"])
			}
			inlineByURL[key] = item
		}
	}
	for _, v := range results {
		item := record(v)
		if inline := inlineByURL[canonical(str(item["url"]))]; inline != nil {
			item["content"] = inline["content"]
			item["fetchedBy"] = inline["provider"]
		}
	}
	if options["includeContent"] == true && options["deferContent"] != true && len(results) > 0 {
		results = fetchContents(toolCtx, results, config)
	}
	return obj{"responseId": responseID(query, options), "query": query, "results": results, "answer": strings.Join(answers, "\n\n"), "runs": runs, "diagnostics": diagnostics}, nil
}

func combineAggregates(query string, aggregates []any, options obj) obj {
	runs, diagnostics := []any{}, []any{}
	answers := []string{}
	byURL := map[string]obj{}
	for _, v := range aggregates {
		aggregate := record(v)
		runs = append(runs, array(aggregate["runs"])...)
		diagnostics = append(diagnostics, array(aggregate["diagnostics"])...)
		if s := str(aggregate["answer"]); s != "" {
			answers = append(answers, rawStr(aggregate["answer"]))
		}
		for _, v := range array(aggregate["results"]) {
			item := record(v)
			key := canonical(str(item["url"]))
			if key == "" {
				key = str(item["url"])
			}
			byURL[key] = item
		}
	}
	results := mergeResults(runs, clamp(options["numResults"], 1, 20, 5))
	for _, v := range results {
		item := record(v)
		if existing := byURL[canonical(str(item["url"]))]; existing != nil {
			for k, v := range existing {
				item[k] = v
			}
		}
	}
	return obj{"responseId": responseID(query, options), "query": query, "results": results, "answer": strings.Join(answers, "\n\n"), "runs": runs, "diagnostics": diagnostics}
}
func sourcePack(result obj, includeContent bool, maxChars int) string {
	items := array(result["results"])
	lines := []string{"Search results for: " + str(result["query"]), fmt.Sprintf("Sources: %d", len(items))}
	for index, v := range items {
		item := record(v)
		lines = append(lines, fmt.Sprintf("%d. %s — %s", index+1, str(item["title"]), str(item["url"])))
		if s := str(item["snippet"]); s != "" {
			lines = append(lines, "   "+compact(s, 500))
		}
		if includeContent && str(item["content"]) != "" {
			lines = append(lines, "   Content: "+compact(item["content"], 1000))
		}
		if providers := stringList(item["providers"]); len(providers) > 0 {
			lines = append(lines, "   Providers: "+strings.Join(providers, ", "))
		}
	}
	if str(result["answer"]) != "" {
		lines = append(lines, "\nProvider notes:\n"+compact(result["answer"], 2500))
	}
	failed := []string{}
	for _, v := range array(result["diagnostics"]) {
		d := record(v)
		if d["ok"] == true {
			continue
		}
		message := str(d["error"])
		if message == "" {
			message = str(d["category"])
		}
		failed = append(failed, "- "+str(d["provider"])+": "+message)
	}
	if len(failed) > 0 {
		lines = append(lines, "\nProvider diagnostics:\n"+strings.Join(failed, "\n"))
	}
	return cut(strings.Join(lines, "\n"), maxChars)
}
func classifySource(u string) obj {
	host := hostOf(u)
	kind, reason := "secondary", "general-web"
	quality := .68
	switch {
	case host == "":
		kind, quality, reason = "unknown", .2, "invalid-url"
	case oneOf(host, "github.com", "gitlab.com", "bitbucket.org") || strings.HasSuffix(host, ".gov") || strings.HasSuffix(host, ".edu"):
		kind, quality, reason = "primary", .95, "primary-host"
	case strings.HasSuffix(host, "wikipedia.org") || regexp.MustCompile(`docs?\.`).MatchString(host):
		kind, quality, reason = "reference", .8, "reference-host"
	case strings.HasSuffix(host, "reddit.com") || strings.HasSuffix(host, "quora.com") || strings.HasSuffix(host, "medium.com"):
		kind, quality, reason = "community", .55, "community-host"
	}
	return obj{"kind": kind, "quality": quality, "reason": reason}
}
func sentences(content string) []string {
	s := compact(content, 50000)
	bounds := regexp.MustCompile(`[.!?。！？]\s+`).FindAllStringIndex(s, -1)
	out := []string{}
	start := 0
	for _, ix := range bounds {
		punct := []rune(s[ix[0]:ix[1]])[0]
		end := ix[0] + len(string(punct))
		piece := strings.TrimSpace(s[start:end])
		if len(utf16.Encode([]rune(piece))) >= 20 {
			out = append(out, piece)
		}
		start = ix[1]
	}
	piece := strings.TrimSpace(s[start:])
	if len(utf16.Encode([]rune(piece))) >= 20 {
		out = append(out, piece)
	}
	return out
}
func buildEvidence(result obj) obj {
	sources := []any{}
	for index, v := range array(result["results"]) {
		item := record(v)
		u := canonical(str(item["url"]))
		if u == "" {
			u = str(item["url"])
		}
		raw := rawStr(item["content"])
		if raw == "" {
			raw = rawStr(item["snippet"])
		}
		passages := []any{}
		ss := sentences(raw)
		for _, sentence := range ss[:min(12, len(ss))] {
			at := strings.Index(raw, sentence)
			start := 0
			if at >= 0 {
				start = len(utf16.Encode([]rune(raw[:at])))
			}
			passages = append(passages, obj{"text": sentence, "start": start, "end": start + len(utf16.Encode([]rune(sentence))), "hash": textHash(sentence)})
		}
		if len(passages) == 0 && str(item["snippet"]) != "" {
			snippet := compact(item["snippet"], 500)
			passages = append(passages, obj{"text": snippet, "start": 0, "end": len(utf16.Encode([]rune(snippet))), "hash": textHash(rawStr(item["snippet"]))})
		}
		provider := item["providers"]
		if provider == nil {
			provider = []any{item["provider"]}
		}
		sources = append(sources, obj{"sourceId": fmt.Sprintf("source-%d", index+1), "url": u, "title": item["title"], "provider": provider, "classification": classifySource(u), "passages": passages})
	}
	return obj{"query": result["query"], "responseId": result["responseId"], "sources": sources}
}
func assessClaims(claims []string, evidence obj) []any {
	out := []any{}
	for _, claim := range claims {
		type candidate struct {
			source, passage obj
			score           float64
		}
		candidates := []candidate{}
		left := tokens(claim)
		for _, v := range array(evidence["sources"]) {
			source := record(v)
			for _, v := range array(source["passages"]) {
				passage := record(v)
				right := tokens(str(passage["text"]))
				shared := 0
				for k := range left {
					if right[k] {
						shared++
					}
				}
				score := 0.0
				if len(left) > 0 && len(right) > 0 {
					score = float64(shared) / float64(len(left))
				}
				if score >= .25 {
					candidates = append(candidates, candidate{source, passage, score})
				}
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
		contradiction := false
		for _, c := range candidates {
			if c.score < .45 {
				continue
			}
			for _, word := range []string{"not", "never", "false", "incorrect", "无", "不是", "错误", "否认", "并非"} {
				if strings.Contains(strings.ToLower(str(c.passage["text"])), word) {
					contradiction = true
				}
			}
		}
		status := "missing-evidence"
		score := 0.0
		if len(candidates) > 0 {
			score = candidates[0].score
			status = "unclear"
			if contradiction {
				status = "contradicted"
			} else if score >= .62 {
				status = "supported"
			}
		}
		sources := []any{}
		for _, c := range candidates[:min(5, len(candidates))] {
			sources = append(sources, obj{"sourceId": c.source["sourceId"], "url": c.source["url"], "passageHash": c.passage["hash"], "score": math.Round(c.score*1000) / 1000})
		}
		out = append(out, obj{"claim": claim, "status": status, "score": math.Round(score*1000) / 1000, "sources": sources, "method": "deterministic-token-overlap"})
	}
	return out
}
func summaryPrompt(result obj) string {
	sources := []string{}
	for index, v := range array(result["results"]) {
		item := record(v)
		parts := []string{fmt.Sprintf("SOURCE %d", index+1), "Title: " + str(item["title"]), "URL: " + str(item["url"]), "Snippet: " + compact(item["snippet"], 900)}
		if str(item["content"]) != "" {
			parts = append(parts, "Body excerpt: "+compact(item["content"], 4000))
		}
		sources = append(sources, strings.Join(parts, "\n"))
	}
	evidence := strings.Join(sources, "\n\n")
	if evidence == "" {
		evidence = "No readable evidence was returned."
	}
	return strings.Join([]string{"Write a concise, factual answer to the user's question using only the supplied sources.", "Do not invent facts, URLs, quotations, or citations. Mention meaningful disagreement or missing evidence.", "Use short headings or bullets when useful and finish with a Sources list containing the URLs used.", "User question: " + str(result["query"]), "\nEvidence:\n" + evidence}, "\n")
}
