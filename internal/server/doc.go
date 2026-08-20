// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. The same process serves the embedded WebUI.
//
// Auth is Bearer token except GET /v1/health. Query ?token= is also accepted.
// Provider CRUD manages the offline registry and credentials; GET /v1/models
// is its flat selectable view. GET /v1/meta exposes the last-used model
// (or the first available fallback), that model's default thinking
// effort, and user home.
// Workspaces live in {KI_HOME}/workspaces.json. Session cwd comes from a
// workspace (or a tmp+ workspace). GET /v1/sessions/{id} includes
// availableSkills / availableMcp (no MCP spawn). PATCH /v1/sessions/{id}
// writes model / thinking effort / title / pin / active leaf / skills / mcp.
// Prompt accepts content blocks and an optional branch parent, then binds MCP
// from the serve-level pool (cached schemas; connect on tool call). GET /v1/fs
// optionally lists files or streams authenticated image, plain-text/code, and
// PDF previews for the attachment picker; POST creates directories.
// Session attachment uploads are content-addressed under that session dir.
// request_header and context_usage events persist on jsonl/SSE.
// Non-/v1 paths serve the SPA at "/"; other unknown paths redirect to "/".
// index.html gets the token injected. The UI is used behind port-forwards.
// A second prompt on a busy session returns 409. message_end awaits jsonl
// append; agent_end may auto-compact. SSE replays runState.evs and drains
// after done.
//
// Skills and AGENTS/CLAUDE context files are cached per session (home, cwd,
// session id). Every successful compaction calls Reload(), which drops both
// caches so the next prompt build re-reads disk. Reload is also the hook a
// future /reload command (or signal) calls.
//
// Routes and run lifecycle: docs/architecture.md.
package server
