// Package mcp reads MCP JSON configs and exposes listed tools as loop.Tool.
//
// Global ~/.ki/.mcp.json plus project <cwd>/.ki/.mcp.json (same server name:
// project wins). Session config only enables/disables names. A failed
// spawn is skipped. This is a minimal JSON-RPC stdio client, not a full SDK.
package mcp
