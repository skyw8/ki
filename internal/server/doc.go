// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. The same process serves the embedded WebUI.
//
// Auth is Bearer token except GET /v1/health. Query ?token= is also accepted.
// GET /v1/models and GET /v1/meta expose catalog/defaults and the user home.
// Workspaces live in {KI_HOME}/workspaces.json. Session cwd comes from a
// workspace (or a tmp+ workspace). PATCH /v1/sessions/{id} writes model /
// title / pin / skills / mcp. GET|POST /v1/fs lists and creates directories.
// request_header events persist the system+tools snapshot on jsonl/SSE.
// Non-/v1 paths serve the SPA at "/"; other unknown paths redirect to "/".
// index.html gets the token injected. The UI is used behind port-forwards.
// A second prompt on a busy session returns 409. message_end awaits jsonl
// append; agent_end may auto-compact. SSE replays runState.evs and drains
// after done.
//
// Routes and run lifecycle: docs/architecture.md.
package server
