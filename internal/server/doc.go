// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. Future WebUI and SDKs use the same routes.
//
// Auth is Bearer token except GET /v1/health. A second prompt on a busy
// session returns 409. message_end awaits jsonl append; agent_end may
// auto-compact. SSE replays runState.evs by cursor and drains after done.
//
// Routes and run lifecycle: docs/architecture.md.
package server
