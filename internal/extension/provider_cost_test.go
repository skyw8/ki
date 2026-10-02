package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"ki/internal/loop"
	"ki/internal/provider"
	"ki/internal/types"
)

type providerCostWriter func([]byte) (int, error)

func (write providerCostWriter) Write(data []byte) (int, error) { return write(data) }

// Keep cost integration checks in memory: process startup and HTTP/SSE parsing
// already have separate coverage, while both host adapters must price the same
// normalized sidecar usage without compiling a second sidecar fixture.
func costTestProvider(t *testing.T, model provider.Model, usage *types.Usage) *ProviderManager {
	t.Helper()
	c := &rpcClient{
		closed:          make(chan struct{}),
		pending:         map[string]chan rpcMsg{},
		providerStreams: map[string]*providerStreamPipe{},
	}
	c.enc = json.NewEncoder(providerCostWriter(func(data []byte) (int, error) {
		var request rpcMsg
		if err := json.Unmarshal(data, &request); err != nil {
			return 0, err
		}
		var result any
		switch request.Method {
		case "provider.stream.start":
			var params struct {
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return 0, err
			}
			// JSON decoding gives the adapter ownership of the usage, just
			// like a real sidecar, without mutating the test's expected data.
			raw, err := json.Marshal(ProviderStreamEvent{
				RequestID: params.RequestID, Type: "done",
				Message: &types.Message{Role: "assistant", StopReason: "stop", Usage: usage},
			})
			if err != nil {
				return 0, err
			}
			var event ProviderStreamEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				return 0, err
			}
			c.providerStreamMu.Lock()
			pipe := c.providerStreams[params.RequestID]
			c.providerStreamMu.Unlock()
			if pipe == nil {
				return 0, fmt.Errorf("missing provider stream %q", params.RequestID)
			}
			pipe.events <- event
			result = map[string]bool{"accepted": true}
		case "provider.compact":
			result = ProviderCompactResult{
				Items: []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`)},
				Usage: usage,
			}
		default:
			return 0, fmt.Errorf("unexpected provider method %q", request.Method)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return 0, err
		}
		c.pendingMu.Lock()
		pending := c.pending[fmt.Sprint(request.ID)]
		c.pendingMu.Unlock()
		if pending == nil {
			return 0, fmt.Errorf("missing provider RPC %v", request.ID)
		}
		pending <- rpcMsg{ID: request.ID, Result: raw}
		return len(data), nil
	}))
	t.Cleanup(c.close)
	pm := NewProviderManager("")
	pm.owners[model.Provider] = "cost-test"
	pm.clients["cost-test"] = c
	return pm
}

func TestProviderAdaptersPriceNormalizedUsage(t *testing.T) {
	base := &provider.Cost{CostRates: provider.CostRates{Input: 2, Output: 10, CacheRead: .2, CacheWrite: 2.5}}
	tiered := &provider.Cost{
		CostRates: base.CostRates,
		Tiers: []provider.CostTier{{
			InputTokensAbove: 272000,
			CostRates:        provider.CostRates{Input: 4, Output: 15, CacheRead: .4, CacheWrite: 5},
		}},
	}
	normal := &types.Usage{Input: 600, Output: 100, CacheRead: 400, CacheWrite: 100, TotalTokens: 1200}
	explicit := &types.Usage{
		Input: 600, Output: 100, CacheRead: 400, CacheWrite: 100, TotalTokens: 1200,
		Cost: &types.UsageCost{Input: .1, Output: .2, CacheRead: .3, CacheWrite: .4, Total: 1},
	}
	cases := []struct {
		name  string
		rates *provider.Cost
		usage *types.Usage
		cost  *types.UsageCost
	}{
		{"cached-input", base, normal, &types.UsageCost{Input: .0012, Output: .001, CacheRead: .00008, CacheWrite: .00025, Total: .00253}},
		{"tier-boundary", tiered, &types.Usage{Input: 71000, Output: 1000, CacheRead: 200000, CacheWrite: 1000, TotalTokens: 273000},
			&types.UsageCost{Input: .142, Output: .01, CacheRead: .04, CacheWrite: .0025, Total: .1945}},
		{"cached-input-selects-tier", tiered, &types.Usage{Input: 71001, Output: 1000, CacheRead: 200000, CacheWrite: 1000, TotalTokens: 273001},
			&types.UsageCost{Input: .284004, Output: .015, CacheRead: .08, CacheWrite: .005, Total: .384004}},
		{"unknown-rates", nil, normal, nil},
		{"explicit-cost", base, explicit, explicit.Cost},
		{"explicit-cost-unknown-rates", nil, explicit, explicit.Cost},
		{"free-model", &provider.Cost{}, normal, &types.UsageCost{}},
		{"missing-usage", base, nil, nil},
	}
	for _, operation := range []string{"stream", "compact"} {
		t.Run(operation, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					model := provider.Model{Provider: "cost-provider", ID: "m", API: "openai-codex-responses", Cost: tc.rates}
					pm := costTestProvider(t, model, tc.usage)
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
					defer cancel()
					var got *types.Usage
					req := loop.Request{Provider: model.Provider, Model: model.ID, API: model.API}
					if operation == "stream" {
						result, err := pm.NewStreamer(model, provider.Credential{Type: provider.AuthNone}).Stream(ctx, req, nil)
						if err != nil {
							t.Fatal(err)
						}
						got = result.Usage
					} else {
						result, err := pm.NewCompactor(model, provider.Credential{Type: provider.AuthNone}).Compact(ctx, req)
						if err != nil {
							t.Fatal(err)
						}
						if len(result.Items) != 1 || string(result.Items[0]) != `{"type":"compaction","encrypted_content":"opaque"}` {
							t.Fatalf("compaction items changed: %s", result.Items)
						}
						got = result.Usage
					}
					if tc.usage == nil {
						if got != nil {
							t.Fatalf("missing usage became %+v", got)
						}
						return
					}
					if got == nil {
						t.Fatal("usage was lost")
					}
					counters := *got
					counters.Cost = tc.usage.Cost
					if !reflect.DeepEqual(counters, *tc.usage) {
						t.Fatalf("normalized counters changed: got %+v, want %+v", got, tc.usage)
					}
					if tc.cost == nil {
						if got.Cost != nil {
							t.Fatalf("unknown rates produced a cost: %+v", got.Cost)
						}
						return
					}
					if got.Cost == nil {
						t.Fatal("normalized sidecar usage was not priced")
					}
					for field, values := range map[string][2]float64{
						"input": {got.Cost.Input, tc.cost.Input}, "output": {got.Cost.Output, tc.cost.Output},
						"cacheRead": {got.Cost.CacheRead, tc.cost.CacheRead}, "cacheWrite": {got.Cost.CacheWrite, tc.cost.CacheWrite},
						"total": {got.Cost.Total, tc.cost.Total},
					} {
						if math.Abs(values[0]-values[1]) > 1e-12 {
							t.Errorf("%s cost = %g, want %g", field, values[0], values[1])
						}
					}
				})
			}
		})
	}
}
