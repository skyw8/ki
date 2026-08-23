// Package mcp reads MCP JSON configs and exposes listed tools as loop.Tool.
//
// Global ~/.ki/.mcp.json plus project <cwd>/.ki/.mcp.json (same server name:
// project wins). A process-wide Toggle (toggles.json) filters names — a
// disabled server is omitted from that turn and is never spawned for it.
//
// Load performs an uncached static config merge; internal/resources pins that
// result per session. Pool lives on ki serve: schemas and connections are
// process-global runtime state, separate from resource snapshots. Bind uses
// cached tools/list; Execute / Prefetch call ensure. URL and
// `npx … mcp-remote <url>` use HTTP so we do not start a tunnel per message.
// Connect failures are skipped. This is a minimal JSON-RPC client, not a full
// SDK.
package mcp
