package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	whatwg "github.com/nlnwa/whatwg-url/url"
	"ki/internal/state"
	"math"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
)

type obj = map[string]any

// Keep JavaScript's original trim/coercion boundaries: BOM is whitespace,
// while NEL is content. Go's default Unicode whitespace set differs.
func jsWhitespace(r rune) bool {
	return r >= '\t' && r <= '\r' || r == ' ' || r == '\uFEFF' || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Zs, r)
}
func jsTrim(s string) string { return strings.TrimFunc(s, jsWhitespace) }
func str(v any) string       { s, _ := v.(string); return jsTrim(s) }
func rawStr(v any) string    { s, _ := v.(string); return s }
func array(v any) []any {
	switch a := v.(type) {
	case []any:
		return a
	case []string:
		out := make([]any, len(a))
		for i, v := range a {
			out[i] = v
		}
		return out
	default:
		return nil
	}
}
func record(v any) obj {
	m, _ := v.(map[string]any)
	if m == nil {
		return obj{}
	}
	return m
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func merge(base, override obj) obj {
	out := clone(base)
	if out == nil {
		out = obj{}
	}
	for k, v := range override {
		a, ok := v.(map[string]any)
		b, prev := out[k].(map[string]any)
		if ok && prev {
			out[k] = merge(b, a)
		} else {
			out[k] = v
		}
	}
	return out
}
func unique(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func stringList(v any) []string {
	out := []string{}
	for _, v := range array(v) {
		if s := str(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}
func oneOf(s string, choices ...string) bool {
	for _, v := range choices {
		if s == v {
			return true
		}
	}
	return false
}

var jsDecimal = regexp.MustCompile(`^[+-]?(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?|Infinity)$`)

func jsArrayString(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = jsArrayString(item)
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(v, ",")
	case map[string]any:
		return "[object Object]"
	default:
		return fmt.Sprint(v)
	}
}
func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return true
	}
}
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		v, _ := n.Float64()
		return v
	case []any:
		return number(jsArrayString(n))
	case []string:
		return number(jsArrayString(n))
	case string:
		s := jsTrim(n)
		if s == "" {
			return 0
		}
		if len(s) > 2 && s[0] == '0' {
			radix := 0
			switch s[1] {
			case 'x', 'X':
				radix = 16
			case 'b', 'B':
				radix = 2
			case 'o', 'O':
				radix = 8
			}
			if radix != 0 {
				if integer, ok := new(big.Int).SetString(s[2:], radix); ok && !strings.ContainsAny(s[2:], "_+-") {
					value, _ := new(big.Float).SetInt(integer).Float64()
					return value
				}
				return math.NaN()
			}
		}
		if jsDecimal.MatchString(s) {
			value, _ := strconv.ParseFloat(s, 64)
			return value
		}
	case bool:
		if n {
			return 1
		}
		return 0
	case nil:
		return 0
	}
	return math.NaN()
}
func clamp(v any, low, high, fallback int) int {
	n := number(v)
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return fallback
	}
	// Clamp before converting: a finite JS number can exceed Go's integer range.
	if n <= float64(low) {
		return low
	}
	if n >= float64(high) {
		return high
	}
	return int(math.Floor(n))
}
func cut(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if n < 0 {
		n = 0
	}
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}
func sliceText(s string, start, end int) string {
	u := utf16.Encode([]rune(s))
	start = max(0, min(start, len(u)))
	end = max(start, min(end, len(u)))
	return string(utf16.Decode(u[start:end]))
}
func compact(v any, n int) string {
	s := strings.Join(strings.FieldsFunc(str(v), jsWhitespace), " ")
	if len(utf16.Encode([]rune(s))) > n {
		return cut(s, max(0, n-3)) + "..."
	}
	return s
}
func hash(v any) string        { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func textHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func rewriteURLQuery(href, query string, remove func(string, string) bool) string {
	type pair struct{ key, value string }
	kept := []pair{}
	changed := false
	for _, field := range strings.Split(query, "&") {
		if field == "" {
			continue
		}
		key, value, _ := strings.Cut(field, "=")
		if decoded, e := url.QueryUnescape(key); e == nil {
			key = decoded
		}
		if decoded, e := url.QueryUnescape(value); e == nil {
			value = decoded
		}
		if remove(key, value) {
			changed = true
			continue
		}
		kept = append(kept, pair{key, value})
	}
	if !changed {
		return href
	}
	// Keep URLSearchParams' insertion order and form-encoding rules. Accessing
	// the Go parser's SearchParams would eagerly rewrite untouched query bytes.
	escape := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "~", "%7E"), "%2A", "*")
	}
	encoded := []string{}
	for _, p := range kept {
		encoded = append(encoded, escape(p.key)+"="+escape(p.value))
	}
	if at := strings.IndexByte(href, '?'); at >= 0 {
		href = href[:at]
	}
	if len(encoded) > 0 {
		href += "?" + strings.Join(encoded, "&")
	}
	return href
}
func queryValue(query, name string) string {
	for _, field := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(field, "=")
		if decoded, e := url.QueryUnescape(key); e == nil {
			key = decoded
		}
		if key == name {
			if decoded, e := url.QueryUnescape(value); e == nil {
				return decoded
			}
			return value
		}
	}
	return ""
}
func canonical(raw string) string {
	// Browser URL semantics also determine source identity and the host checked
	// before content fetches. net/url alone leaves IDNs, encoded dot segments,
	// backslashes, and abbreviated IPv4 addresses unnormalized.
	u, err := whatwg.Parse(raw)
	if err != nil || !oneOf(u.Scheme(), "http", "https") {
		return ""
	}
	u.SetHostname(strings.ToLower(u.Hostname()))
	if u.Scheme() == "https" && u.Port() == "443" || u.Scheme() == "http" && u.Port() == "80" {
		u.SetPort("")
	}
	if len(u.Pathname()) > 1 {
		u.SetPathname(strings.TrimRight(u.Pathname(), "/"))
	}
	return rewriteURLQuery(u.Href(true), u.Query(), func(key, value string) bool {
		lower := strings.ToLower(key)
		return strings.HasPrefix(lower, "utm_") || oneOf(lower, "gclid", "fbclid", "ref", "source")
	})
}

func hostOf(s string) string {
	u, e := whatwg.Parse(s)
	if e != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

var wordPattern = regexp.MustCompile(`[\pL\pN]{2,}`)

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, v := range wordPattern.FindAllString(strings.ToLower(compact(s, 5000)), -1) {
		out[v] = true
	}
	return out
}
func similarity(a, b string) float64 {
	left, right := tokens(a), tokens(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	common := 0
	for k := range left {
		if right[k] {
			common++
		}
	}
	return float64(common) / float64(len(left)+len(right)-common)
}

var domainPattern = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}$`)

func domains(values any) (include, exclude []string) {
	include, exclude = []string{}, []string{}
	for _, raw := range stringList(values) {
		value := jsTrim(strings.TrimPrefix(strings.ToLower(raw), "-"))
		if !strings.Contains(value, "://") {
			value = "https://" + value
		}
		u, e := url.Parse(value)
		domain := ""
		if e != nil {
			domain = strings.Split(strings.Split(jsTrim(strings.TrimPrefix(strings.ToLower(raw), "-")), "/")[0], ":")[0]
		} else {
			domain = u.Hostname()
		}
		domain = strings.Trim(domain, ".")
		if !domainPattern.MatchString(domain) {
			continue
		}
		if strings.HasPrefix(raw, "-") {
			exclude = append(exclude, domain)
		} else {
			include = append(include, domain)
		}
	}
	return unique(include), unique(exclude)
}
func normalizeQueries(args obj) []string {
	many := unique(stringList(args["queries"]))
	if len(many) > 0 {
		return many[:min(8, len(many))]
	}
	if s := str(args["query"]); s != "" {
		return []string{s}
	}
	return nil
}

var providers = []string{"codex", "exa", "tinyfish", "duckduckgo"}
var defaultConfig = obj{"exaMode": "auto", "codexModel": "gpt-6-astra", "codexThinkingEffort": "", "provider": "all", "providerToggles": obj{"codex": true, "exa": true, "tinyfish": true, "duckduckgo": true}, "maxResults": 5, "fetchContent": false, "summaryModel": "openai-codex/gpt-6-astra", "summaryThinkingEffort": "", "summaryGenerationDeadlineMs": 30000, "workflow": "none"}

func normalizeConfig(config obj) obj {
	out := merge(defaultConfig, config)
	out["providerToggles"] = merge(record(defaultConfig["providerToggles"]), record(out["providerToggles"]))
	out["maxResults"] = clamp(out["maxResults"], 1, 20, 5)
	out["summaryGenerationDeadlineMs"] = clamp(out["summaryGenerationDeadlineMs"], 1000, 120000, 30000)
	if !oneOf(rawStr(out["provider"]), "auto", "all", "codex", "exa", "tinyfish", "duckduckgo") {
		out["provider"] = "all"
	}
	if !oneOf(rawStr(out["exaMode"]), "auto", "api", "mcp") {
		out["exaMode"] = "auto"
	}
	if !oneOf(rawStr(out["workflow"]), "none", "auto-summary") {
		out["workflow"] = "none"
	}
	return out
}
func normalizeOptions(args, config obj) (obj, error) {
	provider := config["provider"]
	if _, ok := args["provider"].([]any); ok {
		provider = unique(stringList(args["provider"]))
	} else if oneOf(rawStr(args["provider"]), "auto", "all", "codex", "exa", "tinyfish", "duckduckgo") {
		provider = args["provider"]
	}
	v, exists := args["numResults"]
	if !exists {
		v = math.NaN()
	}
	out := obj{"numResults": clamp(v, 1, 20, clamp(config["maxResults"], 1, 20, 5)), "includeContent": args["includeContent"] == true || config["fetchContent"] == true, "deferContent": args["deferContent"] == true, "domainFilter": stringList(args["domainFilter"]), "provider": provider, "proxy": str(args["proxy"])}
	ds := out["domainFilter"].([]string)
	out["domainFilter"] = ds[:min(100, len(ds))]
	if oneOf(rawStr(args["recencyFilter"]), "day", "week", "month", "year") {
		out["recencyFilter"] = args["recencyFilter"]
	}
	if proxy := str(out["proxy"]); proxy != "" {
		u, e := url.Parse(proxy)
		if e != nil || u.Scheme == "" {
			return nil, fmt.Errorf("proxy must be a valid HTTP(S) URL")
		}
		if !oneOf(u.Scheme, "http", "https") {
			return nil, fmt.Errorf("proxy must use http:// or https://")
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("proxy must include a host")
		}
		out["proxy"] = u.String()
	}
	return out, nil
}
func enabledProviders(config obj, requested any) []string {
	values := stringList(requested)
	if len(values) == 0 {
		v := str(requested)
		if v == "all" || v == "auto" {
			values = providers
		} else {
			values = []string{v}
		}
	}
	selected := []string{}
	for _, v := range values {
		if v == "ddg" {
			v = "duckduckgo"
		}
		if oneOf(v, providers...) {
			selected = append(selected, v)
		}
	}
	selected = unique(selected)
	if len(selected) == 0 {
		selected = providers
	}
	out := []string{}
	toggles := record(config["providerToggles"])
	for _, v := range selected {
		if toggles[v] != false {
			out = append(out, v)
		}
	}
	return out
}
func apiKey(config obj, field, env string) string {
	value := str(config[field])
	if value != "" && value != "<configured>" {
		return value
	}
	return jsTrim(os.Getenv(env))
}

type cacheEntry struct {
	CreatedAt int64 `json:"createdAt"`
	Value     obj   `json:"value"`
}
type searchCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]cacheEntry
	order   []string
	loaded  bool
}

func (c *searchCache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.entries = map[string]cacheEntry{}
	raw, _, e := state.ReadFile(c.path, 1, nil)
	if e != nil {
		return
	}
	var doc struct {
		Entries json.RawMessage `json:"entries"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return
	}
	// Go maps sort their JSON keys, while the original cache evicts in insertion
	// order. Read and write entries in document order to retain that policy.
	decoder := json.NewDecoder(strings.NewReader(string(doc.Entries)))
	if token, e := decoder.Token(); e != nil || token != json.Delim('{') {
		return
	}
	for decoder.More() {
		token, e := decoder.Token()
		if e != nil {
			break
		}
		key, ok := token.(string)
		if !ok {
			break
		}
		var entry cacheEntry
		if decoder.Decode(&entry) != nil {
			c.entries = map[string]cacheEntry{}
			c.order = nil
			return
		}
		c.entries[key] = entry
		c.order = append(c.order, key)
	}

}
func (c *searchCache) get(key string, ttl time.Duration) obj {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	v, ok := c.entries[key]
	if !ok {
		return nil
	}
	if time.Now().UnixMilli()-v.CreatedAt > ttl.Milliseconds() {
		delete(c.entries, key)
		for i, v := range c.order {
			if v == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
		return nil
	}
	return clone(v.Value)
}
func (c *searchCache) put(key string, value obj) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
	}
	c.entries[key] = cacheEntry{time.Now().UnixMilli(), clone(value)}
	for len(c.entries) > 120 && len(c.order) > 0 {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
	fields := make([]string, 0, len(c.order))
	for _, key := range c.order {
		if entry, exists := c.entries[key]; exists {
			encodedKey, _ := json.Marshal(key)
			encodedEntry, _ := json.Marshal(entry)
			fields = append(fields, string(encodedKey)+":"+string(encodedEntry))
		}
	}
	_ = state.WriteVersioned(c.path, 1, obj{"version": 1, "entries": json.RawMessage("{" + strings.Join(fields, ",") + "}")}, 0600)
}
func cacheKey(query string, options obj) string {
	return hash(obj{"query": query, "numResults": options["numResults"], "includeContent": options["includeContent"], "deferContent": options["deferContent"] == true, "recencyFilter": str(options["recencyFilter"]), "domainFilter": options["domainFilter"], "provider": options["provider"]})
}
func responseID(query string, options obj) string {
	return hash(obj{"query": query, "options": options, "at": time.Now().UnixMilli() / 60000})
}
func loadConfig(root string) (obj, error) {
	raw, _, e := state.ReadFile(filepath.Join(root, "config.json"), 1, nil)
	if os.IsNotExist(e) {
		return normalizeConfig(nil), nil
	}
	if e != nil {
		return nil, fmt.Errorf("deep-web-search config is invalid: %w", e)
	}
	var saved obj
	if json.Unmarshal(raw, &saved) != nil || saved == nil {
		return nil, fmt.Errorf("deep-web-search config is invalid: config must be an object")
	}
	delete(saved, "version")
	return normalizeConfig(saved), nil
}
