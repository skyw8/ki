package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"ki/internal/loop"
	"ki/internal/session"
)

// Why this fallback exists: completed SSE logs have a finite lifetime and
// budget. A late CLI reader must print durable replies instead of interpreting
// an unavailable replay as a successful empty answer. Recovery never prompts.
func recoverLatestTurn(ctx context.Context, base, token, id string) error {
	path := "/v1/sessions/" + url.PathEscape(id)
	var detail struct {
		LeafID  string                `json:"leafId"`
		Running bool                  `json:"running"`
		Turns   []session.CompactTurn `json:"compactTurns"`
	}
	if err := doJSONContext(ctx, base, token, http.MethodGet, path+"?view=compact&keep=0", nil, &detail); err != nil {
		return err
	}
	if detail.Running {
		return fmt.Errorf("%w: replay changed while recovering; attach to the running session again", errHTTPResponse)
	}
	if len(detail.Turns) == 0 {
		return nil
	}
	var indexed struct {
		Index []session.IndexEntry `json:"index"`
	}
	if err := doJSONContext(ctx, base, token, http.MethodGet, path+"?fields=index", nil, &indexed); err != nil {
		return err
	}
	byID := make(map[string]session.IndexEntry, len(indexed.Index))
	for _, e := range indexed.Index {
		byID[e.ID] = e
	}
	boundary := detail.Turns[len(detail.Turns)-1].ID
	var ids []string
	seen := map[string]bool{}
	reachedBoundary := false
	for leaf := detail.LeafID; leaf != "" && !seen[leaf]; {
		seen[leaf] = true
		e, ok := byID[leaf]
		if !ok {
			return fmt.Errorf("%w: recovery branch changed", errHTTPResponse)
		}
		if e.Role == "assistant" {
			ids = append(ids, e.ID)
		}
		if leaf == boundary {
			reachedBoundary = true
			break
		}
		leaf = e.ParentID
	}
	if !reachedBoundary {
		return fmt.Errorf("%w: recovery turn changed", errHTTPResponse)
	}
	slices.Reverse(ids)
	var printer streamPrinter
	for len(ids) > 0 {
		count := min(len(ids), session.MaxViewBatch)
		var escaped []string
		for _, id := range ids[:count] {
			escaped = append(escaped, url.QueryEscape(id))
		}
		var bodies struct {
			Entries []session.Entry `json:"entries"`
		}
		if err := doJSONContext(ctx, base, token, http.MethodGet, path+"?entries="+strings.Join(escaped, ","), nil, &bodies); err != nil {
			return err
		}
		if len(bodies.Entries) != count {
			return fmt.Errorf("%w: recovery entries disappeared", errHTTPResponse)
		}
		for _, e := range bodies.Entries {
			printer.event(loop.Event{Type: loop.MessageEnd, Message: e.Message})
		}
		ids = ids[count:]
	}
	return nil
}
