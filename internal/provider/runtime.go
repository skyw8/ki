package provider

import (
	"ki/internal/loop"
)

// Streamer is the provider-facing alias of loop.Streamer.
type Streamer = loop.Streamer

// Runtime creates a streamer for one resolved model and credential.
// Provider-specific request and response semantics belong behind this
// boundary, instead of being added to loop's generic request shape.
type Runtime interface {
	ProviderID() string
	NewStreamer(model Model, credential Credential) Streamer
}
