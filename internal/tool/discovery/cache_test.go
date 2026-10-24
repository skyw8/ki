package discovery

import (
	"context"
	"fmt"
	toolapi "ki/internal/tool"
	"testing"
)

type countedTool struct {
	calls int
	name  string
}

func (t *countedTool) DiscoveryCacheIdentity() any                          { return t }
func (t *countedTool) Name() string                                         { return t.name }
func (*countedTool) Description() string                                    { return "cached search" }
func (*countedTool) Prompt() string                                         { return "" }
func (*countedTool) Snippet() string                                        { return "" }
func (t *countedTool) Parameters() map[string]any                           { t.calls++; return map[string]any{"type": "object"} }
func (*countedTool) Execute(context.Context, map[string]any) toolapi.Result { return toolapi.Result{} }

func TestCacheReusesIdentityAndBoundsProjections(t *testing.T) {
	var cache Cache
	a, b := &countedTool{name: "first"}, &countedTool{name: "second"}
	full := []toolapi.Tool{a, b}
	first := cache.Get(full)
	if cache.Get(full) != first || a.calls != 1 || b.calls != 1 {
		t.Fatal("unchanged directory rebuilt")
	}
	filtered := cache.Get(full[:1])
	if filtered == first || len(filtered.Names()) != 1 || cache.Get(full) != first {
		t.Fatal("filtered snapshot leaked or replaced live catalog")
	}
	replacement := &countedTool{name: "first"}
	if cache.Get([]toolapi.Tool{replacement, b}) == first {
		t.Fatal("new revision reused old directory")
	}
	for i := 0; i < 20; i++ {
		cache.Get([]toolapi.Tool{&countedTool{name: fmt.Sprintf("revision_%d", i)}})
	}
	if len(cache.entries) > 8 || len(first.Names()) != 2 {
		t.Fatal("cache unbounded or eviction mutated snapshot")
	}
}

func BenchmarkCatalogCold(b *testing.B) {
	tools := make([]toolapi.Tool, 4096)
	for i := range tools {
		tools[i] = &countedTool{name: fmt.Sprintf("document_%d", i)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		New(tools)
	}
}

func BenchmarkCatalogCacheHit(b *testing.B) {
	tools := make([]toolapi.Tool, 4096)
	for i := range tools {
		tools[i] = &countedTool{name: fmt.Sprintf("document_%d", i)}
	}
	var cache Cache
	cache.Get(tools)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		cache.Get(tools)
	}
}
