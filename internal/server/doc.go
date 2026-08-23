// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. The same process serves the embedded WebUI.
//
// Auth is Bearer token except GET /v1/health. Query ?token= is also accepted.
// Provider CRUD manages the offline registry and credentials; GET /v1/models
// is its flat selectable view. GET /v1/meta exposes the last-used model
// (or the first available fallback), that model's default thinking
// effort, and user home.
// Workspaces live in {KI_HOME}/workspaces.json. Session cwd comes from a
// workspace (or a tmp+ workspace). GET /v1/sessions/{id} includes a
// read-only catalog (availableSkills / availableMcp with cached MCP tools,
// commands[]). PATCH /v1/sessions/{id} writes model / thinking / title /
// pin / leaf. Skills and MCP enablement is {KI_HOME}/toggles.json via
// GET/PATCH /v1/skills and /v1/mcp.
// Prompt accepts content blocks and an optional branch parent, then binds MCP
// from the serve-level pool (cached schemas; connect on tool call). GET /v1/fs
// optionally lists files or streams authenticated image, plain-text/code, and
// PDF previews for the attachment picker; POST creates directories.
// Session attachment uploads are content-addressed under that session dir.
// request_header, context_usage, and streamed apply_patch preview events
// persist on jsonl/SSE.
// Non-/v1 paths serve the SPA at "/"; other unknown paths redirect to "/".
// index.html gets the token injected. The UI is used behind port-forwards.
// A second prompt on a busy session returns 409. message_end awaits jsonl
// append; agent_end may auto-compact. SSE replays runState.evs and drains
// after done.
//
// One server-owned resources.Loader atomically caches runtime environment,
// skills, AGENTS/CLAUDE, prompt templates, and merged .mcp.json by session id
// only. Settings scans are uncached. Reload() (POST /v1/reload, /reload,
// compaction, toggle PATCH) invalidates every snapshot and closes the MCP pool
// so the next prompt/GET rebuilds resources.
//
// Routes and run lifecycle: docs/architecture.md.
package server
