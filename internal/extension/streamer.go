package extension

import (
	"context"
	"net/http"
	"reflect"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

type occupyStreamer struct {
	inner   loop.Streamer
	items   []namedInterceptor
	skipped *skipSet
	onErr   func(name, capability, code, message string)
}

func wrapStreamer(inner loop.Streamer, items []namedInterceptor, skipped *skipSet, onErr func(name, capability, code, message string)) loop.Streamer {
	if inner == nil || len(items) == 0 {
		return inner
	}
	has := false
	for _, it := range items {
		if it.hasSync(EventBeforeProviderRequest) {
			has = true
			break
		}
	}
	if !has {
		return inner
	}
	if skipped == nil {
		skipped = newSkipSet()
	}
	return &occupyStreamer{inner: inner, items: items, skipped: skipped, onErr: onErr}
}

func cannedStop(emit func(loop.AssistantDelta) error, text string) (types.Message, error) {
	acc := types.Message{Role: "assistant", StopReason: "stop", Content: []types.Content{{Type: "text", Text: text}}}
	if emit != nil {
		_ = emit(loop.AssistantDelta{Type: "text_delta", Delta: text, Partial: acc})
	}
	return acc, nil
}

func (s *occupyStreamer) Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error) {
	live := req
	for _, it := range s.items {
		if s.skipped.has(it.name) || !it.hasSync(EventBeforeProviderRequest) {
			continue
		}
		view := ProviderRequest{
			Messages:       redactMessages(live.Messages),
			Tools:          live.Tools,
			Provider:       live.Provider,
			Model:          live.Model,
			MaxTokens:      live.MaxTokens,
			ThinkingEffort: live.ThinkingEffort,
		}
		next, sc, err := it.inner.BeforeProvider(ctx, view)
		if err != nil {
			if it.failClosed {
				return cannedStop(emit, "extension "+it.name+" failed closed")
			}
			if s.onErr != nil {
				s.onErr(it.name, string(CapLifecycle), EventBeforeProviderRequest, err.Error())
			}
			s.skipped.mark(it.name)
			continue
		}
		if sc != nil && sc.Text != "" {
			return cannedStop(emit, sc.Text)
		}
		applyProviderMutations(&live, view, next)
	}
	msg, err := s.inner.Stream(ctx, live, emit)
	if err == nil {
		return msg, nil
	}
	if ctx.Err() != nil {
		return msg, err
	}
	for _, it := range s.items {
		if s.skipped.has(it.name) || !it.hasSync(EventBeforeProviderRequest) {
			continue
		}
		fb, ferr := it.inner.AfterProviderError(ctx, err.Error())
		if ferr != nil {
			if s.onErr != nil {
				s.onErr(it.name, string(CapLifecycle), EventProviderError, ferr.Error())
			}
			s.skipped.mark(it.name)
			continue
		}
		if fb.Text != "" {
			return cannedStop(emit, fb.Text)
		}
		if fb.Skip {
			return msg, err
		}
	}
	return msg, err
}

// applyProviderMutations copies interceptor-returned Model/Provider/tools onto
// the live request. Messages stay the original (unredacted) slice unless the
// interceptor actually changed them: the view strips image Data, and writing
// that view back would drop attachments before Live.Stream.
func applyProviderMutations(live *loop.Request, view, next ProviderRequest) {
	live.Provider = next.Provider
	live.Model = next.Model
	live.Tools = next.Tools
	live.MaxTokens = next.MaxTokens
	live.ThinkingEffort = next.ThinkingEffort
	if next.Messages != nil && !reflect.DeepEqual(next.Messages, view.Messages) {
		live.Messages = next.Messages
	}
}

type headerDoer struct {
	inner   provider.HTTPDoer
	items   []namedInterceptor
	skipped *skipSet
	onErr   func(name, capability, code, message string)
}

func wrapHTTPDoer(inner provider.HTTPDoer, items []namedInterceptor, skipped *skipSet, onErr func(name, capability, code, message string)) provider.HTTPDoer {
	if inner == nil {
		inner = http.DefaultClient
	}
	has := false
	for _, it := range items {
		if it.hasSync(EventBeforeProviderHeaders) {
			has = true
			break
		}
	}
	if !has {
		return inner
	}
	return &headerDoer{inner: inner, items: items, skipped: skipped, onErr: onErr}
}

func (d *headerDoer) Do(req *http.Request) (*http.Response, error) {
	view := viewHTTP(req)
	for _, it := range d.items {
		if d.skipped.has(it.name) || !it.hasSync(EventBeforeProviderHeaders) {
			continue
		}
		patch, err := it.inner.BeforeProviderHTTP(req.Context(), view)
		if err != nil {
			if d.onErr != nil {
				d.onErr(it.name, string(CapLifecycle), EventBeforeProviderHeaders, err.Error())
			}
			d.skipped.mark(it.name)
			continue
		}
		if patch.URL != "" && req.URL != nil {
			if u, err := req.URL.Parse(patch.URL); err == nil {
				req.URL = u
			}
		}
		for k, v := range patch.Headers {
			if secretHTTPHeader(k) {
				continue
			}
			// Empty value is delete: Header.Set("",) would leave a present
			// empty header instead of removing the name.
			if v == "" {
				req.Header.Del(k)
				continue
			}
			req.Header.Set(k, v)
		}
	}
	resp, err := d.inner.Do(req)
	if err != nil || resp == nil {
		return resp, err
	}
	headers := map[string]string{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	for _, it := range d.items {
		if d.skipped.has(it.name) || !it.hasSync(EventBeforeProviderHeaders) {
			continue
		}
		_ = it.inner.AfterProviderHTTP(req.Context(), resp.StatusCode, headers)
	}
	return resp, nil
}
