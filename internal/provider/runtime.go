package provider

import (
	"context"

	"ki/internal/loop"
	"ki/internal/types"
)

// ProviderStreamer is the provider-specific execution boundary. A runtime
// may be in-process or backed by a sidecar; loop only sees loop.Streamer.
type ProviderStreamer interface {
	Stream(ctx context.Context, req loop.Request, emit func(loop.AssistantDelta) error) (types.Message, error)
}

// ProviderRuntime creates a streamer for one resolved model and credential.
// Provider-specific request and response semantics belong behind this
// boundary, instead of being added to loop's generic request shape.
type ProviderRuntime interface {
	ProviderID() string
	NewStreamer(model Model, credential Credential) ProviderStreamer
}
