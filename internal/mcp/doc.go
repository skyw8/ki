// Package mcp reads MCP JSON configs and exposes listed tools as loop.Tool.
//
// Global ~/.ki/.mcp.json plus project <cwd>/.ki/.mcp.json (same server name:
// project wins). Session Toggle only filters names — a disabled server is
// omitted from that turn and is never spawned for it.
//
// Pool lives on ki serve, not on a session. MCP servers here are process-global
// (~/.ki/.mcp.json). Spawning per prompt (or per session) re-paid npx/HTTP
// handshake on every message and blocked agent_start. Bind therefore uses
// cached tools/list schemas only; Execute/Prefetch call ensure. URL and
// `npx … mcp-remote <url>` use HTTP so we do not start a tunnel per message.
// Connect failures are skipped. This is a minimal JSON-RPC client, not a full SDK.
package mcp
