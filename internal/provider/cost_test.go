package provider

import (
	"testing"

	"ki/internal/types"
)

func TestCalculateCostNormalizesOpenAIInput(t *testing.T) {
	m := Model{API: "responses", Cost: &Cost{CostRates: CostRates{Input: 2, Output: 10, CacheRead: .2}}}
	u := &types.Usage{Input: 1000, Output: 100, CacheRead: 400}
	CalculateCost(m, u)
	if u.Input != 600 || u.TotalTokens != 1100 {
		t.Fatalf("usage = %+v", u)
	}
	if u.Cost == nil || u.Cost.Total != .00228 {
		t.Fatalf("cost = %+v", u.Cost)
	}
}

func TestCalculateCostKeepsAnthropicInput(t *testing.T) {
	m := Model{API: "anthropic", Cost: &Cost{CostRates: CostRates{Input: 1, CacheRead: .1, CacheWrite: 1.25}}}
	u := &types.Usage{Input: 600, CacheRead: 400, CacheWrite: 100}
	CalculateCost(m, u)
	if u.Input != 600 || u.TotalTokens != 1100 {
		t.Fatalf("usage = %+v", u)
	}
}
