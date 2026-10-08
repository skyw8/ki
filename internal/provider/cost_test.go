package provider

import (
	"math"
	"reflect"
	"testing"

	"ki/internal/types"
)

func TestBuiltinGPTStandardPricing(t *testing.T) {
	// Snapshot of OpenAI Standard pricing verified on 2026-10-08. Explicit
	// rates catch accidental use of Batch/Flex/Fast prices or lost cache fees.
	cases := []struct {
		id         string
		base, long CostRates
	}{
		{"gpt-6.1-sol", CostRates{Input: 2, Output: 10, CacheRead: .1, CacheWrite: 2.5}, CostRates{Input: 4, Output: 15, CacheRead: .2, CacheWrite: 5}},
		{"gpt-6-astra", CostRates{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5}, CostRates{Input: 20, Output: 75, CacheRead: 2, CacheWrite: 25}},
		{"gpt-6-luna", CostRates{Input: .1, Output: .5, CacheRead: .01, CacheWrite: .125}, CostRates{Input: .2, Output: .75, CacheRead: .02, CacheWrite: .25}},
		{"gpt-5.6-sol", CostRates{Input: 4, Output: 20, CacheRead: .4, CacheWrite: 5}, CostRates{Input: 8, Output: 30, CacheRead: .8, CacheWrite: 10}},
		{"gpt-5.6-terra", CostRates{Input: 2, Output: 12, CacheRead: .2, CacheWrite: 2.5}, CostRates{Input: 4, Output: 18, CacheRead: .4, CacheWrite: 5}},
		{"gpt-5.6-luna", CostRates{Input: .2, Output: 1.2, CacheRead: .02, CacheWrite: .25}, CostRates{Input: .4, Output: 1.8, CacheRead: .04, CacheWrite: .5}},
	}
	models := map[string]Model{}
	for _, p := range BuiltinProviders() {
		if p.ID == "openai" {
			for _, model := range p.Models {
				models[model.ID] = model
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			model, ok := models[tc.id]
			if !ok {
				t.Fatal("model missing from built-in catalog")
			}
			want := &Cost{CostRates: tc.base, Tiers: []CostTier{{InputTokensAbove: 272000, CostRates: tc.long}}}
			if !reflect.DeepEqual(model.Cost, want) {
				t.Fatalf("cost = %+v, want %+v", model.Cost, want)
			}
			for _, input := range []int{272000, 272001} {
				// Cached input is part of the threshold too; normalization
				// must not turn a long-context request into a short one.
				u := &types.Usage{Input: input, Output: 1000, CacheRead: 200000, CacheWrite: 1000}
				CalculateCost(model, u)
				rates := tc.base
				if input > 272000 {
					rates = tc.long
				}
				if u.Input != input-201000 || u.TotalTokens != input+1000 {
					t.Fatalf("normalized usage = %+v", u)
				}
				cost := types.UsageCost{
					Input:      float64(input-201000) * rates.Input / 1e6,
					Output:     1000 * rates.Output / 1e6,
					CacheRead:  200000 * rates.CacheRead / 1e6,
					CacheWrite: 1000 * rates.CacheWrite / 1e6,
				}
				cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
				if u.Cost == nil || math.Abs(u.Cost.Total-cost.Total) > 1e-12 ||
					u.Cost.Input != cost.Input || u.Cost.Output != cost.Output ||
					u.Cost.CacheRead != cost.CacheRead || u.Cost.CacheWrite != cost.CacheWrite {
					t.Fatalf("input=%d: cost = %+v, want %+v", input, u.Cost, cost)
				}
			}
		})
	}
}

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
