package provider

import (
	"context"
	"encoding/json"

	"ki/internal/loop"
	"ki/internal/types"
)

// Streamer is the provider-facing alias of loop.Streamer.
type Streamer = loop.Streamer

// CompactResult is the canonical provider context returned by a standalone
// remote compaction.
type CompactResult struct {
	Items []json.RawMessage
	Usage *types.Usage
}

// Compactor is an optional provider runtime capability. Implementations must
// return the complete ordered canonical window, not only its compaction item.
type Compactor interface {
	Compact(context.Context, loop.Request) (CompactResult, error)
}

// Runtime creates a streamer for one resolved model and credential.
// Provider-specific request and response semantics belong behind this
// boundary, instead of being added to loop's generic request shape.
type Runtime interface {
	ProviderID() string
	NewStreamer(model Model, credential Credential) Streamer
}
