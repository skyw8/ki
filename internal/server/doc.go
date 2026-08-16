// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. The same process serves the embedded WebUI.
//
// Auth is Bearer token except GET /v1/health. Query ?token= is also accepted.
// Non-/v1 paths serve the SPA; index.html gets the token injected.
// A second prompt on a busy session returns 409. message_end awaits jsonl
// append; agent_end may auto-compact. SSE replays runState.evs and drains
// after done.
//
// Routes and run lifecycle: docs/architecture.md.
package server
