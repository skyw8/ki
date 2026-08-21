// Package mcp reads MCP JSON configs and exposes listed tools as loop.Tool.
//
// Global ~/.ki/.mcp.json plus project <cwd>/.ki/.mcp.json (same server name:
// project wins). A process-wide Toggle (toggles.json) filters names — a
// disabled server is omitted from that turn and is never spawned for it.
//
// Cached(home, cwd, sessionID) memoizes the merged file per session so a
// prompt does not re-read .mcp.json; a new session re-reads. InvalidateAll
// (server.Reload) drops that table. Pool lives on ki serve: schemas and
// connections are process-global. Bind uses cached tools/list; Execute /
// Prefetch call ensure. URL and `npx … mcp-remote <url>` use HTTP so we do
// not start a tunnel per message. Connect failures are skipped. This is a
// minimal JSON-RPC client, not a full SDK.
package mcp
