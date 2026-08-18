package provider

import "ki/internal/types"

// CalculateCost normalizes overlapping provider counters and records USD cost.
// OpenAI-compatible APIs report cached tokens as part of input_tokens, whereas
// Anthropic reports input_tokens as the uncached remainder.
func CalculateCost(model Model, usage *types.Usage) {
	if usage == nil {
		return
	}
	if model.API != "anthropic" {
		usage.Input = max(0, usage.Input-usage.CacheRead-usage.CacheWrite)
	}
	usage.TotalTokens = usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
	if model.Cost == nil {
		return
	}
	rates := model.Cost.CostRates
	inputTotal := usage.Input + usage.CacheRead + usage.CacheWrite
	for _, tier := range model.Cost.Tiers {
		if inputTotal > tier.InputTokensAbove {
			rates = tier.CostRates
		}
	}
	const million = 1_000_000
	cost := &types.UsageCost{
		Input:      float64(usage.Input) * rates.Input / million,
		Output:     float64(usage.Output) * rates.Output / million,
		CacheRead:  float64(usage.CacheRead) * rates.CacheRead / million,
		CacheWrite: float64(usage.CacheWrite) * rates.CacheWrite / million,
	}
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	usage.Cost = cost
}
