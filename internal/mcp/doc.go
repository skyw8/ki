// Package mcp reads MCP JSON configs and exposes listed tools as loop.Tool.
//
// Global ~/.ki/.mcp.json is the only configuration source. A process-wide
// Toggle (toggles.json) filters names — a disabled server is omitted from that
// turn and is never spawned for it.
//
// Load performs an uncached static config read; internal/resources pins the
// global config and per-session discovered tool catalog. Manager owns only
// live, session-isolated official SDK ClientSessions. Opening a session
// (create, GET by id, fork) starts Prepare in the background; List does not
// spawn.
// Prompt preparation connects enabled servers in parallel (reuse if already
// warmed) and completes tools/list before model request assembly; one server
// failure does not hide successful servers or stop the prompt. URL selects
// Streamable HTTP and command selects stdio; ambiguous specs are rejected.
// Tool-list changes remain stale until explicit reload.
// Cross-package ownership, events, and reload lifecycle: docs/mcp.md.
package mcp
