package main

import (
	"slices"
	"time"
)

type coolEntry struct {
	at  time.Time
	ttl time.Duration
}
type freeRouter struct {
	models                []string
	exhaustedTTL, slowTTL time.Duration
	exhausted             map[string]coolEntry
}

func milliseconds(n uint64) time.Duration { return time.Duration(n) * time.Millisecond }
func newFreeRouter(models []string, exhaustedTTLMS, slowTTLMS uint64) *freeRouter {
	return &freeRouter{models: models, exhaustedTTL: milliseconds(exhaustedTTLMS), slowTTL: milliseconds(slowTTLMS), exhausted: map[string]coolEntry{}}
}
func (r *freeRouter) setModels(models []string) {
	r.models = models
	for id := range r.exhausted {
		if !slices.Contains(models, id) {
			delete(r.exhausted, id)
		}
	}
}
func (r *freeRouter) nextModels(count int) []string {
	result := []string{}
	now := time.Now()
	for _, id := range r.models {
		if len(result) >= count {
			break
		}
		if e, ok := r.exhausted[id]; ok {
			if now.Sub(e.at) < e.ttl {
				continue
			}
			delete(r.exhausted, id)
		}
		result = append(result, id)
	}
	return result
}
func (r *freeRouter) markExhausted(id string) {
	if slices.Contains(r.models, id) {
		r.exhausted[id] = coolEntry{time.Now(), r.exhaustedTTL}
	}
}
func (r *freeRouter) markSlow(id string) {
	if !slices.Contains(r.models, id) {
		return
	}
	if e, ok := r.exhausted[id]; ok && e.ttl >= r.exhaustedTTL {
		return
	}
	r.exhausted[id] = coolEntry{time.Now(), r.slowTTL}
}
