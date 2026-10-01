package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	whatwg "github.com/nlnwa/whatwg-url/url"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const providerSearchBudget = 15 * time.Second
const codexSearchBudget = 60 * time.Second
const contentBudget = 20 * time.Second
const providerGrace = 2 * time.Second
const queryBudget = 65 * time.Second
const toolBudget = 110 * time.Second

func searchBudget(provider string) time.Duration {
	if provider == "codex" {
		return codexSearchBudget
	}
	return providerSearchBudget
}

type settled struct {
	Value   any
	Err     error
	Present bool
}

func settleWithGrace(ctx context.Context, tasks []func() (any, error), quorum int, grace time.Duration, onGrace func()) []settled {
	out := make([]settled, len(tasks))
	if len(tasks) == 0 {
		return out
	}
	type result struct {
		i int
		v any
		e error
	}
	ch := make(chan result, len(tasks))
	for i, task := range tasks {
		go func() { v, e := task(); ch <- result{i, v, e} }()
	}
	remaining, fulfilled := len(tasks), 0
	var timer *time.Timer
	var graceCh <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for remaining > 0 {
		select {
		case <-ctx.Done():
			if onGrace != nil {
				onGrace()
			}
			return out
		case <-graceCh:
			if onGrace != nil {
				onGrace()
			}
			return out
		case r := <-ch:
			out[r.i] = settled{r.v, r.e, true}
			remaining--
			if r.e == nil {
				fulfilled++
			}
			if remaining > 0 && fulfilled >= quorum && timer == nil {
				timer = time.NewTimer(grace)
				graceCh = timer.C
			}
		}
	}
	return out
}

var httpClient = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}

func request(ctx context.Context, method, endpoint string, headers map[string]string, body any, budget time.Duration) ([]byte, int, http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var input io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			input = strings.NewReader(v)
		default:
			b, e := json.Marshal(v)
			if e != nil {
				return nil, 0, nil, e
			}
			input = bytes.NewReader(b)
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, input)
	if e != nil {
		return nil, 0, nil, e
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := httpClient.Do(req)
	if e != nil {
		return nil, 0, nil, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 65*1024*1024+1))
	if len(b) > 65*1024*1024 {
		return nil, resp.StatusCode, resp.Header, fmt.Errorf("response exceeds 65 MiB")
	}
	return b, resp.StatusCode, resp.Header, e
}
func jsonRequest(ctx context.Context, method, endpoint string, headers map[string]string, body any, budget time.Duration, label string) (obj, error) {
	raw, status, _, e := request(ctx, method, endpoint, headers, body, budget)
	if e != nil {
		return nil, e
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s-http-%d: %s", label, status, compact(string(raw), 260))
	}
	var out obj
	if json.Unmarshal(raw, &out) != nil {
		return nil, fmt.Errorf("%s-invalid-response: response was not JSON", label)
	}
	return out, nil
}
func privateHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || host == "::1" || host == "::" || strings.HasPrefix(host, "fc") || strings.HasPrefix(host, "fd") || strings.HasPrefix(host, "fe80:") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return true
		}
		if v := ip.To4(); v != nil && (v[0] == 0 || v[0] >= 224) {
			return true
		}
	}
	return false
}
func safeFetchURL(raw string) (*url.URL, error) {
	u, e := whatwg.Parse(raw)
	if e != nil {
		return nil, e
	}
	if !oneOf(u.Scheme(), "http", "https") {
		return nil, fmt.Errorf("content-url-invalid: only HTTP(S) URLs are allowed")
	}
	if u.Username() != "" || u.Password() != "" {
		return nil, fmt.Errorf("content-url-invalid: credentialed URLs are not allowed")
	}
	if privateHost(strings.Trim(u.Hostname(), "[]")) {
		return nil, fmt.Errorf("content-url-blocked: local or private hosts are not allowed")
	}
	return url.Parse(u.String())
}

var tags = regexp.MustCompile(`<[^>]*>`)

var entities = regexp.MustCompile(`(?i)&(#x?[0-9a-f]+|amp|lt|gt|quot|apos|nbsp);`)

func decodeEntities(s string) string {
	// The extension's excerpts use its limited entity table, not the browser's
	// complete HTML decoder (which also remaps numeric control characters).
	return entities.ReplaceAllStringFunc(s, func(entity string) string {
		value := strings.ToLower(entity[1 : len(entity)-1])
		if named, ok := map[string]string{"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": " "}[value]; ok {
			return named
		}
		digits, radix := value[1:], 10
		if strings.HasPrefix(value, "#x") {
			digits, radix = value[2:], 16
		} else {
			// JavaScript parseInt accepts the decimal prefix of e.g. &#123abc;.
			for i, c := range digits {
				if c < '0' || c > '9' {
					digits = digits[:i]
					break
				}
			}
		}
		if code, e := strconv.ParseUint(digits, radix, 32); e == nil && code <= 0x10ffff {
			return string(rune(code))
		}
		return entity
	})
}
func stripTags(s string) string {
	return strings.Join(strings.FieldsFunc(decodeEntities(tags.ReplaceAllString(s, " ")), jsWhitespace), " ")
}
func htmlToText(raw string) (title, content string) {
	titlePattern := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	if m := titlePattern.FindStringSubmatch(raw); len(m) > 0 {
		title = strings.Join(strings.FieldsFunc(tags.ReplaceAllString(decodeEntities(m[1]), " "), jsWhitespace), " ")
	}
	body := raw
	for _, tag := range []string{"script", "style", "noscript", "svg", "template"} {
		body = regexp.MustCompile(`(?is)<`+tag+`[^>]*>.*?</`+tag+`>`).ReplaceAllString(body, " ")
	}
	body = regexp.MustCompile(`(?i)<br\s*/?\s*>|</(?:p|div|li|article|section|h[1-6]|tr)>`).ReplaceAllString(body, "\n")
	body = decodeEntities(tags.ReplaceAllString(body, " "))
	body = regexp.MustCompile(`[ \t\r\f]+`).ReplaceAllString(body, " ")
	body = regexp.MustCompile(`\n[\t-\r \x{FEFF}\x{2028}\x{2029}\p{Zs}]+`).ReplaceAllString(body, "\n")
	body = regexp.MustCompile(`\n{3,}`).ReplaceAllString(body, "\n\n")
	return title, jsTrim(body)
}
func fetchOneContent(ctx context.Context, rawURL string) obj {
	requested := canonical(rawURL)
	if requested == "" {
		requested = rawURL
	}
	fail := func(e error) obj {
		return obj{"url": requested, "title": "", "content": "", "error": e.Error(), "fetchedBy": "http"}
	}
	u, e := safeFetchURL(requested)
	if e != nil {
		return fail(e)
	}
	ctx, cancel := context.WithTimeout(ctx, contentBudget)
	defer cancel()
	for redirects := 0; redirects <= 5; redirects++ {
		req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if e != nil {
			return fail(e)
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain;q=0.8")
		req.Header.Set("User-Agent", "ki-deep-web-search/0.1")
		resp, e := httpClient.Do(req)
		if e != nil {
			return fail(e)
		}
		if oneOf(fmt.Sprint(resp.StatusCode), "301", "302", "303", "307", "308") && resp.Header.Get("Location") != "" {
			resp.Body.Close()
			if redirects == 5 {
				return fail(fmt.Errorf("content-redirect-limit: too many redirects"))
			}
			location, e := whatwg.ParseRef(u.String(), resp.Header.Get("Location"))
			if e != nil {
				return fail(e)
			}
			u, e = safeFetchURL(location.String())
			if e != nil {
				return fail(e)
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fail(fmt.Errorf("content-http-%d: source returned HTTP %d", resp.StatusCode, resp.StatusCode))
		}
		if resp.ContentLength > 5*1024*1024 {
			resp.Body.Close()
			return fail(fmt.Errorf("content-too-large: response exceeds 5 MiB"))
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024+1))
		resp.Body.Close()
		if e != nil {
			return fail(e)
		}
		if len(b) > 5*1024*1024 {
			return fail(fmt.Errorf("content-too-large: response exceeds 5 MiB"))
		}
		raw := string(b)
		title := ""
		content := jsTrim(raw)
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") || regexp.MustCompile(`(?i)<html[\s>]`).MatchString(raw) {
			title, content = htmlToText(raw)
		}
		if content == "" {
			return fail(fmt.Errorf("content-empty: source contained no readable text"))
		}
		final := canonical(u.String())
		if final == "" {
			final = u.String()
		}
		return obj{"url": final, "title": compact(title, 240), "content": content, "error": nil, "fetchedBy": "http"}
	}
	return fail(fmt.Errorf("content-redirect-limit: too many redirects"))
}
func fetchContents(ctx context.Context, results []any, config obj) []any {
	ctx, cancel := context.WithTimeout(ctx, contentBudget)
	defer cancel()
	queue := make(chan obj, len(results))
	for _, v := range results[:min(20, len(results))] {
		queue <- record(v)
	}
	close(queue)
	out := []any{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				merged := clone(item)
				if str(item["content"]) != "" {
					merged["error"] = nil
				} else {
					fetched := fetchOneContent(ctx, str(item["url"]))
					if str(fetched["content"]) == "" && record(config["providerToggles"])["tinyfish"] != false && apiKey(config, "tinyfishApiKey", "TINYFISH_API_KEY") != "" {
						alternatives, e := fetchTinyfish(ctx, []string{str(item["url"])}, config)
						if e == nil && len(alternatives) > 0 && str(record(alternatives[0])["content"]) != "" {
							fetched = record(alternatives[0])
							fetched["error"] = nil
							fetched["fetchedBy"] = str(fetched["provider"])
						}
					}
					for k, v := range fetched {
						merged[k] = v
					}
				}
				mu.Lock()
				out = append(out, merged)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	byURL := map[string]obj{}
	for _, v := range out {
		item := record(v)
		key := canonical(str(item["url"]))
		if key == "" {
			key = str(item["url"])
		}
		byURL[key] = item
	}
	final := make([]any, len(results))
	for i, v := range results {
		item := record(v)
		key := canonical(str(item["url"]))
		if key == "" {
			key = str(item["url"])
		}
		if fetched := byURL[key]; fetched != nil {
			final[i] = fetched
		} else {
			final[i] = clone(v)
		}
	}
	return final
}
