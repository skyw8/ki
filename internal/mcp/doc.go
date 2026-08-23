// Package mcp reads MCP JSON configs and exposes listed tools as loop.Tool.
//
// Global ~/.ki/.mcp.json plus project <cwd>/.ki/.mcp.json (same server name:
// project wins). A process-wide Toggle (toggles.json) filters names — a
// disabled server is omitted from that turn and is never spawned for it.
//
// Load performs an uncached static config merge; internal/resources pins the
// config and discovered tool catalog per session. Manager owns only live,
// session-isolated official SDK ClientSessions. Prompt preparation connects
// enabled servers in parallel and completes tools/list before model request
// assembly; one server failure does not hide successful servers or stop the
// prompt. URL selects Streamable HTTP and command selects stdio; ambiguous
// specs are rejected. Tool-list changes remain stale until explicit reload.
// Cross-package ownership, events, and reload lifecycle: docs/mcp.md.
package mcp
