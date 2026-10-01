package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type pool struct {
	mu         sync.Mutex
	config     config
	discovery  *modelDiscovery
	router     *freeRouter
	client     *http.Client
	lastAPIKey string
}

func newPool(ctx context.Context, c config, client *http.Client) *pool {
	p := &pool{config: c, client: client, discovery: &modelDiscovery{client: client}}
	go p.backgroundRefresh(ctx)
	return p
}
func (p *pool) snapshot() config { p.mu.Lock(); defer p.mu.Unlock(); return p.config }
func modelIDs(models []modelInfo) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}
func (p *pool) ensure(ctx context.Context, key string) ([]modelInfo, error) {
	c := p.snapshot()
	models, err := p.discovery.ensure(ctx, key, c.BaseURL)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.router == nil {
		p.router = newFreeRouter(modelIDs(models), p.config.ExhaustedTTLMS, p.config.SlowTTLMS)
	} else {
		p.router.setModels(modelIDs(models))
	}
	return models, nil
}
func (p *pool) updateConfig(c config) {
	models := p.discovery.cached()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = c
	if models != nil {
		p.router = newFreeRouter(modelIDs(models), c.ExhaustedTTLMS, c.SlowTTLMS)
	}
}
func (p *pool) resolveKey(credential string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := resolveAPIKey(p.config.APIKey, credential)
	if key != "" {
		p.lastAPIKey = key
	}
	return p.lastAPIKey
}
func (p *pool) backgroundRefresh(ctx context.Context) {
	for {
		c := p.snapshot()
		timer := time.NewTimer(milliseconds(c.RefreshIntervalMS))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		key := c.APIKey
		if key == "" {
			key = envAPIKey()
		}
		if key == "" {
			continue
		}
		if err := p.discovery.refresh(ctx, key, c.BaseURL); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "[freerouter] model list refresh failed: %v\n", err)
		}
	}
}
func batchTimeoutMS(c config, batch int) uint64 {
	switch {
	case batch <= 1:
		return c.FirstTokenTimeoutMS
	case batch == 2:
		return (c.FirstTokenTimeoutMS*3 + 1) / 2
	default:
		return c.FirstTokenTimeoutMS * 2
	}
}

type raceInput struct {
	Messages, Tools []any
	MaxTokens       uint64
	PinnedModel     string
}

type batchOutcome struct {
	winner                                                    string
	exhausted                                                 []string
	fatal                                                     string
	timedOut, cancelled, stalled, winnerStreamError, hasFatal bool
}
type modelEvent struct {
	index    int
	event    object
	result   error
	finished bool
}

func nextRaceEvent(ctx context.Context, events <-chan modelEvent, timeout <-chan time.Time) (m modelEvent, timedOut, cancelled bool) {
	select {
	case <-ctx.Done():
	case <-timeout:
		timedOut = true
	case m = <-events:
	}
	// Cancellation also closes the upstream reader, so its queued completion can
	// win select alongside Done. Recheck at the receive boundary before treating
	// that completion as a stalled stream or a deadline as a model cooldown.
	if ctx.Err() != nil {
		return m, false, true
	}
	return m, timedOut, false
}

func runRace(ctx context.Context, p *pool, input raceInput, key string, emit func(object), sidecar bool) {
	if key == "" {
		emit(streamError("error", "No OpenRouter API key configured. Set it via PUT /v1/providers/free-router/credential, the extension config apiKey, or OPENROUTER_API_KEY."))
		return
	}
	models, err := p.ensure(ctx, key)
	if err != nil {
		if ctx.Err() != nil {
			emit(streamError("aborted", "Request was cancelled."))
		} else {
			emit(streamError("error", "freerouter: "+err.Error()))
		}
		return
	}
	if input.PinnedModel != "" {
		runSingle(ctx, p, input, key, emit, sidecar)
		return
	}
	offset := 0
	thinking := ""
	think := func(delta string) {
		thinking += delta
		if sidecar {
			emit(event("thinking_delta", object{"contentIndex": 0, "delta": delta}))
		}
	}
	if sidecar {
		offset = 1
		emit(event("thinking_start", object{"contentIndex": 0}))
		think("Searching free models...")
	}
	c := p.snapshot()
	tried := map[string]bool{}
	for batch := 1; batch <= int(c.MaxBatches); batch++ {
		if ctx.Err() != nil {
			emit(streamError("aborted", "Request was cancelled."))
			return
		}
		candidates := []string{}
		p.mu.Lock()
		for _, id := range p.router.nextModels(len(models)) {
			if !tried[id] {
				candidates = append(candidates, id)
				if len(candidates) >= int(c.RaceWidth) {
					break
				}
			}
		}
		p.mu.Unlock()
		if len(candidates) == 0 {
			break
		}
		for _, id := range candidates {
			tried[id] = true
		}
		fmt.Fprintf(os.Stderr, "[freerouter] racing: %s\n", strings.Join(candidates, ", "))
		if sidecar {
			think(fmt.Sprintf("\nRound %d: %s", batch, strings.Join(candidates, ", ")))
		}
		outcome := raceBatch(ctx, p, input, key, candidates, batch, emit, offset, sidecar, &thinking)
		p.mu.Lock()
		for _, id := range outcome.exhausted {
			p.router.markExhausted(id)
		}
		p.mu.Unlock()
		if outcome.cancelled {
			emit(streamError("aborted", "Request was cancelled."))
			return
		}
		if outcome.hasFatal {
			if sidecar {
				think("\n" + outcome.fatal)
			}
			emit(streamError("error", outcome.fatal))
			return
		}
		if outcome.winner != "" || outcome.stalled || outcome.winnerStreamError {
			return
		}
		p.mu.Lock()
		for _, id := range candidates {
			if outcome.timedOut {
				p.router.markSlow(id)
			} else {
				p.router.markExhausted(id)
			}
		}
		p.mu.Unlock()
	}
	message := "All free models exhausted. They will recover automatically — please try again in a moment."
	if sidecar {
		think("\n" + message)
	}
	emit(streamError("error", message))
}
func runSingle(ctx context.Context, p *pool, input raceInput, key string, emit func(object), sidecar bool) {
	offset := 0
	thinking := ""
	if sidecar {
		offset = 1
		thinking = "Using " + input.PinnedModel
		emit(event("thinking_start", object{"contentIndex": 0}))
		emit(event("thinking_delta", object{"contentIndex": 0, "delta": thinking}))
	}
	_ = streamFreeModel(ctx, p.client, input.PinnedModel, input, key, p.snapshot().BaseURL, func(ev object) {
		switch str(ev["type"]) {
		case "start":
			return
		case "done":
			if sidecar {
				ev["message"] = prependThinking(obj(ev["message"]), thinking)
			}
		}
		emit(remapIndex(ev, offset))
	})
}
func raceBatch(ctx context.Context, p *pool, input raceInput, key string, candidates []string, batch int, emit func(object), offset int, sidecar bool, thinking *string) batchOutcome {
	c := p.snapshot()
	batchCtx, cancelBatch := context.WithCancel(ctx)
	defer cancelBatch()
	// An event bus is shared by readers and their final results. Every result is
	// enqueued after its events, so a completed empty stream is retired promptly
	// and fatal/exhausted classifications cannot race with channel teardown.
	merged := make(chan modelEvent, 64)
	cancels := make([]context.CancelFunc, len(candidates))
	for idx, id := range candidates {
		child, stop := context.WithCancel(batchCtx)
		cancels[idx] = stop
		go func(idx int, id string) {
			send := func(m modelEvent) {
				select {
				case merged <- m:
				case <-batchCtx.Done():
				}
			}
			err := streamFreeModel(child, p.client, id, input, key, c.BaseURL, func(ev object) { send(modelEvent{index: idx, event: ev}) })
			send(modelEvent{index: idx, result: err, finished: true})
		}(idx, id)
	}
	buffers := make([][]object, len(candidates))
	active := make(map[int]bool, len(candidates))
	for i := range candidates {
		active[i] = true
	}
	out := batchOutcome{}
	deadline := time.NewTimer(milliseconds(batchTimeoutMS(c, batch)))
	defer deadline.Stop()
	classify := func(m modelEvent) {
		var e *modelError
		if errors.As(m.result, &e) {
			switch e.Kind {
			case "exhausted":
				out.exhausted = append(out.exhausted, e.ModelID)
			case "fatal":
				if !out.hasFatal {
					out.fatal = e.Message
					out.hasFatal = true
				}
			}
		}
	}
	forward := func(ev object) {
		if str(ev["type"]) == "done" && sidecar {
			ev["message"] = prependThinking(obj(ev["message"]), strings.TrimSpace(*thinking))
		}
		if str(ev["type"]) != "start" {
			emit(remapIndex(ev, offset))
		}
	}
	for len(active) > 0 {
		m, timedOut, cancelled := nextRaceEvent(ctx, merged, deadline.C)
		if cancelled {
			out.cancelled = true
			return out
		}
		if timedOut {
			out.timedOut = true
			return out
		}
		if m.finished {
			classify(m)
			delete(active, m.index)
			if out.hasFatal {
				return out
			}
			continue
		}
		if !active[m.index] {
			continue
		}
		ev := m.event
		if !qualifies(ev) {
			// Error classification follows in the same producer's result. Waiting for
			// that result preserves account-fatal errors instead of trying another key.
			if str(ev["type"]) != "error" {
				buffers[m.index] = append(buffers[m.index], ev)
			}
			continue
		}
		idx := m.index
		out.winner = candidates[idx]
		for j, stop := range cancels {
			if j != idx {
				stop()
			}
		}
		for _, prior := range buffers[idx] {
			forward(prior)
		}
		if sidecar {
			delta := "\nUsing " + candidates[idx]
			*thinking += delta
			emit(event("thinking_delta", object{"contentIndex": 0, "delta": delta}))
		}
		forward(ev)
		if str(ev["type"]) == "done" {
			return out
		}
		idle := time.NewTimer(milliseconds(c.IdleTimeoutMS))
		defer idle.Stop()
		for {
			m, timedOut, cancelled := nextRaceEvent(ctx, merged, idle.C)
			if cancelled {
				out.cancelled = true
				return out
			}
			if timedOut {
				out.stalled = true
				emit(streamError("error", candidates[idx]+" stream stalled"))
				return out
			}
			if m.finished {
				classify(m)
				if m.index == idx {
					out.stalled = true
					emit(streamError("error", candidates[idx]+" stream stalled"))
					return out
				}
				continue
			}
			if m.index != idx {
				continue
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(milliseconds(c.IdleTimeoutMS))
			forward(m.event)
			switch str(m.event["type"]) {
			case "done":
				return out
			case "error":
				out.winnerStreamError = true
				return out
			}
		}
	}
	return out
}
