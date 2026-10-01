package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"ki/pkg/extensionrpc"
)

//go:embed tools.json
var toolData []byte
var toolSpecs []any

func init() {
	if e := json.Unmarshal(toolData, &toolSpecs); e != nil {
		panic(e)
	}
}

type rpcCall func(context.Context, string, any, any) error
type searchApp struct {
	root     string
	cache    *searchCache
	peer     *extensionrpc.Peer
	call     rpcCall
	search   providerSearch
	configMu sync.Mutex
	config   obj
	ctx      context.Context
	cancel   context.CancelFunc
	bg       sync.WaitGroup
}

func newApp(root string, peer *extensionrpc.Peer) *searchApp {
	ctx, cancel := context.WithCancel(context.Background())
	return &searchApp{root: root, peer: peer, call: peer.Call, cache: &searchCache{path: filepath.Join(root, "cache.json")}, search: searchProvider, ctx: ctx, cancel: cancel}
}
func (a *searchApp) loadConfig() (obj, error) {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	if a.config != nil {
		return clone(a.config), nil
	}
	config, e := loadConfig(a.root)
	if e == nil {
		a.config = config
	}
	return clone(config), e
}
func (a *searchApp) progress(ctx context.Context, toolCallID string, partial obj) {
	id := extensionrpc.RequestID(ctx)
	if id == "" || a.peer == nil {
		return
	}
	_ = a.peer.Notify("tool.progress", obj{"id": id, "toolCallId": toolCallID, "partial": partial})
}
func (a *searchApp) runSearch(ctx context.Context, query string, options, config obj, toolCallID string) (obj, error) {
	key := cacheKey(query, options)
	if cached := a.cache.get(key, 10*time.Minute); cached != nil {
		a.progress(ctx, toolCallID, obj{"phase": "cache", "status": "hit", "query": query})
		cached["cacheHit"] = true
		return cached, nil
	}
	result, e := aggregateSearch(ctx, query, options, config, a.search, func(p obj) { p["query"] = query; a.progress(ctx, toolCallID, p) })
	if e != nil {
		return nil, e
	}
	a.cache.put(key, result)
	a.cache.put("response:"+str(result["responseId"]), result)
	result["cacheHit"] = false
	return result, nil
}
func sourceResult(s string, details obj, isError bool) obj {
	out := obj{"content": []any{obj{"type": "text", "text": s}}, "details": details}
	if isError {
		out["isError"] = true
	}
	return out
}
func detailsFor(workflow, effective string, result, extra obj) obj {
	runs := []any{}
	for _, v := range array(result["runs"]) {
		run := record(v)
		entry := obj{"provider": run["provider"], "count": len(array(run["results"])), "durationMs": run["durationMs"]}
		if run["transport"] != nil {
			entry["transport"] = run["transport"]
		}
		runs = append(runs, entry)
	}
	details := obj{"responseId": result["responseId"], "requestedWorkflow": workflow, "effectiveWorkflow": effective, "fallbackTo": nil, "fallbackReason": nil, "query": result["query"], "providerRuns": runs, "diagnostics": result["diagnostics"], "cacheHit": result["cacheHit"] == true}
	for k, v := range extra {
		details[k] = v
	}
	return details
}
func (a *searchApp) appendEntry(ctx context.Context, sessionID, customType string, data obj) {
	if sessionID == "" || a.call == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = a.call(ctx, "session.appendEntry", obj{"sessionId": sessionID, "customType": customType, "data": data}, nil)
}
func (a *searchApp) persistContent(ctx context.Context, sessionID string, result obj) {
	content := []any{}
	for _, v := range array(result["results"]) {
		item := record(v)
		if rawStr(item["content"]) != "" {
			content = append(content, obj{"url": item["url"], "title": item["title"], "content": cut(rawStr(item["content"]), 20000)})
		}
	}
	if len(content) > 0 {
		a.appendEntry(ctx, sessionID, "deep-web-search-content-ready", obj{"responseId": result["responseId"], "contents": content})
	}
}
func (a *searchApp) persistSearch(ctx context.Context, sessionID string, result, details obj) {
	results := []any{}
	for _, v := range array(result["results"]) {
		item := record(v)
		e := str(item["error"])
		if e == "" {
			e = str(item["contentError"])
		}
		results = append(results, obj{"title": item["title"], "url": item["url"], "snippet": item["snippet"], "providers": item["providers"], "ranks": item["ranks"], "score": item["score"], "content": cut(rawStr(item["content"]), 20000), "contentError": e})
	}
	a.appendEntry(ctx, sessionID, "deep-web-search-results", obj{"responseId": result["responseId"], "query": result["query"], "results": results, "diagnostics": result["diagnostics"], "details": details})
	a.persistContent(ctx, sessionID, result)
}
func contentFromResult(result, args obj) string {
	filter := map[string]bool{}
	_, filtered := args["urls"].([]any)
	for _, v := range array(args["urls"]) {
		if s, ok := v.(string); ok {
			filter[s] = true
		}
	}
	chunks := []string{}
	for _, v := range array(result["results"]) {
		item := record(v)
		if filtered && !filter[rawStr(item["url"])] {
			continue
		}
		content := rawStr(item["content"])
		if find := jsArrayString(args["findText"]); truthy(args["findText"]) && content != "" {
			at := strings.Index(strings.ToLower(content), strings.ToLower(find))
			if at >= 0 {
				index := len(utf16.Encode([]rune(content[:at])))
				content = sliceText(content, max(0, index-500), index+2500)
			}
		}
		offset, limit := 0.0, 8000.0
		if value, exists := args["offset"]; exists {
			n := number(value)
			if !math.IsNaN(n) && !math.IsInf(n, 0) {
				offset = math.Max(0, n)
			}
		}
		if value, exists := args["limit"]; exists {
			n := number(value)
			if !math.IsNaN(n) && !math.IsInf(n, 0) {
				limit = math.Max(1, math.Min(50000, n))
			}
		}
		// String.slice truncates the two endpoints separately; truncating the
		// fractional limit first loses a character when offset+limit crosses it.
		length := float64(len(utf16.Encode([]rune(content))))
		content = sliceText(content, int(math.Min(length, offset)), int(math.Min(length, offset+limit)))
		if content == "" {
			detail := ""
			if str(item["contentError"]) != "" {
				detail = ": " + str(item["contentError"])
			}
			content = "[no content" + detail + "]"
		}
		chunks = append(chunks, rawStr(item["title"])+"\n"+rawStr(item["url"])+"\n"+content)
	}
	return strings.Join(chunks, "\n\n")
}
func (a *searchApp) fetchTool(ctx context.Context, args, config obj) obj {
	urls := []string{}
	if s, ok := args["url"].(string); ok {
		urls = append(urls, s)
	}
	for _, v := range array(args["urls"]) {
		if s, ok := v.(string); ok {
			urls = append(urls, s)
		}
	}
	urls = unique(urls)
	urls = urls[:min(20, len(urls))]
	if len(urls) == 0 {
		return sourceResult("fetch_content requires url or urls", obj{}, true)
	}
	items := []any{}
	for _, u := range urls {
		items = append(items, obj{"url": u, "title": u, "snippet": ""})
	}
	results := fetchContents(ctx, items, config)
	lines, fetched := []string{}, []any{}
	for _, v := range results {
		item := record(v)
		title := str(item["title"])
		if title == "" {
			title = str(item["url"])
		}
		content := rawStr(item["content"])
		body := cut(content, 6000)
		if body == "" {
			message := str(item["error"])
			if message == "" {
				message = "empty"
			}
			body = "ERROR: " + message
		}
		lines = append(lines, title+"\n"+str(item["url"])+"\n"+body)
		e := any(nil)
		if str(item["error"]) != "" {
			e = item["error"]
		}
		by := any(nil)
		if str(item["fetchedBy"]) != "" {
			by = item["fetchedBy"]
		}
		fetched = append(fetched, obj{"url": item["url"], "bytes": len(utf16.Encode([]rune(content))), "error": e, "fetchedBy": by})
	}
	return sourceResult(strings.Join(lines, "\n\n"), obj{"urls": urls, "fetched": fetched}, false)
}
func (a *searchApp) getContent(args obj) obj {
	response := obj(nil)
	if id, ok := args["responseId"].(string); ok {
		response = a.cache.get("response:"+id, 24*time.Hour)
	}
	if response == nil {
		return sourceResult("get_search_content: responseId was not found or has expired", obj{}, true)
	}
	urls := args["urls"]
	if !truthy(urls) {
		urls = nil
	}
	offset, limit := args["offset"], args["limit"]
	if !truthy(offset) {
		offset = 0
	}
	if !truthy(limit) {
		limit = 8000
	}
	return sourceResult(contentFromResult(response, args), obj{"responseId": args["responseId"], "urls": urls, "offset": offset, "limit": limit}, false)
}
func (a *searchApp) sourceCheck(ctx context.Context, args, config obj, toolCallID string) (obj, error) {
	query := str(args["query"])
	claims := array(args["claims"])
	if claim, ok := args["claim"].(string); ok {
		claims = append(claims, claim)
	}
	list := stringList(claims)
	list = list[:min(20, len(list))]
	if query == "" || len(list) == 0 {
		return sourceResult("source_check requires query and claim or claims", obj{}, true), nil
	}
	options, e := normalizeOptions(merge(args, obj{"includeContent": true}), config)
	if e != nil {
		return nil, e
	}
	result, e := a.runSearch(ctx, query, options, config, toolCallID)
	if e != nil {
		return nil, e
	}
	evidence := buildEvidence(result)
	assessments := assessClaims(list, evidence)
	b, _ := json.MarshalIndent(obj{"query": query, "assessments": assessments}, "", "  ")
	return sourceResult(string(b), obj{"responseId": result["responseId"], "evidence": evidence, "assessments": assessments, "diagnostics": result["diagnostics"]}, false), nil
}
func (a *searchApp) deepSearch(ctx context.Context, args, config obj, toolCallID, sessionID string) (obj, error) {
	queries := normalizeQueries(args)
	if len(queries) == 0 {
		return sourceResult("deep_web_search requires query or queries", obj{}, true), nil
	}
	options, e := normalizeOptions(args, config)
	if e != nil {
		return nil, e
	}
	options["deferContent"] = options["includeContent"]
	started := time.Now()
	workflow := str(config["workflow"])
	tasks := []func() (any, error){}
	for _, query := range queries {
		tasks = append(tasks, func() (any, error) {
			a.progress(ctx, toolCallID, obj{"phase": "query", "status": "running", "query": query})
			aggregate, e := a.runSearch(ctx, query, options, config, toolCallID)
			if e != nil {
				a.progress(ctx, toolCallID, obj{"phase": "query", "status": "failed", "query": query, "error": e.Error()})
				return nil, e
			}
			a.progress(ctx, toolCallID, obj{"phase": "query", "status": "done", "query": query, "count": len(array(aggregate["results"]))})
			return aggregate, nil
		})
	}
	settled := settleWithGrace(ctx, tasks, len(tasks)+1, time.Hour, nil)
	aggregates, failures := []any{}, []any{}
	for index, item := range settled {
		if item.Present && item.Err == nil {
			aggregates = append(aggregates, item.Value)
			continue
		}
		message := "tool call cancelled"
		if item.Err != nil {
			message = item.Err.Error()
		}
		failures = append(failures, obj{"query": queries[index], "error": message})
	}
	if len(aggregates) == 0 {
		parts := []string{}
		for _, v := range failures {
			item := record(v)
			parts = append(parts, str(item["query"])+": "+str(item["error"]))
		}
		return nil, fmt.Errorf("query-failed: %s", strings.Join(parts, "; "))
	}
	result := record(aggregates[0])
	if len(queries) > 1 {
		result = combineAggregates(strings.Join(queries, "; "), aggregates, options)
	}
	for _, v := range failures {
		item := record(v)
		result["diagnostics"] = append(array(result["diagnostics"]), obj{"query": item["query"], "ok": false, "category": "query", "error": item["error"]})
	}
	a.cache.put("response:"+str(result["responseId"]), result)
	if options["includeContent"] == true {
		missing := false
		for _, v := range array(result["results"]) {
			if str(record(v)["content"]) == "" {
				missing = true
			}
		}
		if missing {
			copy := clone(result)
			cfg := clone(config)
			budget := contentBudget
			if deadline, ok := ctx.Deadline(); ok {
				budget = min(budget, time.Until(deadline))
			}
			hydrationCtx, cancel := context.WithTimeout(a.ctx, budget)
			// Honor a cancel while the tool is still running, but keep hydration
			// alive after a successful reply closes the RPC request context.
			stop := context.AfterFunc(ctx, cancel)
			defer stop()
			a.bg.Add(1)
			go func() {
				defer a.bg.Done()
				defer cancel()
				ctx := hydrationCtx
				copy["results"] = fetchContents(ctx, array(copy["results"]), cfg)
				a.cache.put("response:"+str(copy["responseId"]), copy)
				a.persistContent(ctx, sessionID, copy)
			}()
		}
	}
	extra := obj{"searchDurationMs": time.Since(started).Milliseconds(), "sourceCount": len(array(result["results"])), "queryFailures": failures}
	output := sourcePack(result, false, 12000)
	effective := "none"
	if workflow == "none" {
		available := false
		for _, v := range array(result["results"]) {
			available = available || str(record(v)["content"]) != ""
		}
		extra["contentAvailable"] = available
	} else if workflow == "auto-summary" {
		a.progress(ctx, toolCallID, obj{"phase": "summary", "status": "running"})
		summaryStarted := time.Now()
		answer, model, e := completeWithModel(ctx, summaryPrompt(result), str(config["summaryModel"]), str(config["summaryThinkingEffort"]), time.Duration(clamp(config["summaryGenerationDeadlineMs"], 1000, 120000, 30000))*time.Millisecond)
		if e != nil {
			extra["fallbackTo"] = "none"
			extra["fallbackReason"] = e.Error()
			extra["summary"] = obj{"fallbackUsed": true, "fallbackReason": e.Error(), "phase": "summary"}
		} else {
			effective = "auto-summary"
			output = answer
			extra["summary"] = obj{"model": model, "durationMs": time.Since(summaryStarted).Milliseconds(), "fallbackUsed": false, "phase": "summary"}
		}
		extra["searchDurationMs"] = time.Since(started).Milliseconds()
	} else {
		return nil, fmt.Errorf("workflow-unsupported: %s", workflow)
	}
	details := detailsFor(workflow, effective, result, extra)
	a.persistSearch(ctx, sessionID, result, details)
	return sourceResult(output, details, false), nil
}
func (a *searchApp) handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	p := obj{}
	_ = json.Unmarshal(raw, &p)
	switch method {
	case "initialize":
		return obj{"tools": toolSpecs}, nil
	case "config.updated":
		a.configMu.Lock()
		a.config = nil
		a.configMu.Unlock()
		return obj{}, nil
	case "shutdown":
		a.cancel()
		return obj{}, nil
	case "tool.execute":
		config, e := a.loadConfig()
		if e != nil {
			return nil, e
		}
		name := rawStr(p["name"])
		if !oneOf(name, "deep_web_search", "fetch_content", "get_search_content", "source_check") {
			return sourceResult("unknown tool "+name, obj{}, true), nil
		}
		ctx, cancel := context.WithTimeout(ctx, toolBudget)
		defer cancel()
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
		args := record(p["args"])
		var result obj
		switch name {
		case "get_search_content":
			result = a.getContent(args)
		case "fetch_content":
			result = a.fetchTool(ctx, args, config)
		case "source_check":
			result, e = a.sourceCheck(ctx, args, config, rawStr(p["toolCallId"]))
		case "deep_web_search":
			result, e = a.deepSearch(ctx, args, config, rawStr(p["toolCallId"]), rawStr(p["sessionId"]))
		}
		if e != nil {
			return sourceResult(e.Error(), obj{"error": e.Error()}, true), nil
		}
		return result, nil
	default:
		return obj{}, nil
	}
}
func main() {
	root := os.Getenv("KI_EXTENSION_ROOT")
	if root == "" {
		root, _ = os.Getwd()
	}
	peer := extensionrpc.New(os.Stdout)
	app := newApp(root, peer)
	if os.Getenv("KI_DEEP_WEB_SEARCH_VALIDATE_CONFIG") == "1" {
		if _, e := app.loadConfig(); e != nil {
			fmt.Fprintln(os.Stderr, filepath.Join(root, "config.json")+":", e)
		}
	}
	if e := peer.Serve(context.Background(), os.Stdin, app.handle); e != nil {
		fmt.Fprintln(os.Stderr, "deep-web-search:", e)
	}
	app.cancel()
	app.bg.Wait()
}
