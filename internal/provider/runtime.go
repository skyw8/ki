package provider

import (
	"context"
	"encoding/json"

	"ki/internal/loop"
	"ki/internal/types"
)

type Streamer = loop.Streamer

type CompactResult struct {
	Items []json.RawMessage
	Usage *types.Usage
}

type Compactor interface {
	Compact(context.Context, loop.Request) (CompactResult, error)
}

type Runtime interface {
	ProviderID() string
	NewStreamer(model Model, credential Credential) Streamer
}
