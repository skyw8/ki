// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. The same process serves the embedded WebUI.
//
// Auth is Bearer token except GET /v1/health. Query ?token= is also accepted.
// GET /v1/models and GET /v1/meta expose in-process catalog/defaults.
// PATCH /v1/sessions/{id} writes model/skills/mcp. request_header events
// persist the system+tools snapshot on the existing jsonl/SSE path.
// Non-/v1 paths serve the SPA; index.html gets the token injected.
// A second prompt on a busy session returns 409. message_end awaits jsonl
// append; agent_end may auto-compact. SSE replays runState.evs and drains
// after done.
//
// Routes and run lifecycle: docs/architecture.md.
package server
