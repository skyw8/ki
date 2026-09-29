package session

import (
	"context"
	"strings"
	"unicode/utf8"
)

// SearchOptions controls transcript text search.
type SearchOptions struct {
	Context       context.Context
	Query         string
	Limit         int
	IncludeAgents bool
	CWD           string
}

// SearchHit identifies a matching session and a short surrounding excerpt.
type SearchHit struct {
	Info
	Snippet string `json:"snippet,omitempty"`
}

// Search scans human and assistant text, including visible thinking summaries.
// It returns the first match per session in newest-session order.
func Search(root string, opts SearchOptions) ([]SearchHit, bool, error) {
	query := strings.TrimSpace(strings.ReplaceAll(opts.Query, "\x00", ""))
	if query == "" {
		return nil, false, nil
	}
	if utf8.RuneCountInString(query) > 500 {
		query = string([]rune(query)[:500])
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	infos, err := List(root)
	if err != nil {
		return nil, false, err
	}
	var out []SearchHit
	for _, info := range infos {
		if opts.Context != nil && opts.Context.Err() != nil {
			return nil, false, opts.Context.Err()
		}
		if !opts.IncludeAgents && info.ForkMode == ForkModeTree {
			continue
		}
		if opts.CWD != "" && info.CWD != opts.CWD {
			continue
		}
		sess, err := Open(info.Dir)
		if err != nil {
			continue
		}
		snippet, ok := searchSnippet(sess.Entries(), sess.LeafID(), strings.ToLower(query))
		_ = sess.Close()
		if !ok {
			continue
		}
		if len(out) >= limit {
			return out, true, nil
		}
		out = append(out, SearchHit{Info: info, Snippet: snippet})
	}
	return out, false, nil
}

func searchSnippet(entries []Entry, leafID, query string) (string, bool) {
	for _, e := range LeafChain(entries, leafID) {
		if e.Type != "message" || e.Message == nil || (e.Message.Role != "user" && e.Message.Role != "assistant") {
			continue
		}
		var b strings.Builder
		b.WriteString(e.Message.Text())
		for _, c := range e.Message.Content {
			if c.Thinking != "" {
				b.WriteByte('\n')
				b.WriteString(c.Thinking)
			}
		}
		if snippet, ok := snippetAround(b.String(), query); ok {
			return snippet, true
		}
	}
	return "", false
}

func snippetAround(text, query string) (string, bool) {
	low := strings.ToLower(text)
	at := strings.Index(low, query)
	if at < 0 {
		return "", false
	}
	runes := []rune(text)
	prefix := []rune(text[:at])
	start := max(len(prefix)-40, 0)
	end := min(len(prefix)+utf8.RuneCountInString(query)+40, len(runes))
	s := strings.Join(strings.Fields(string(runes[start:end])), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s, true
}
