// Package toggles stores global built-in tool, MCP server, skill, and extension enablement
// plus the busy-message delivery default in {KI_HOME}/toggles.json.
//
// Discovery still uses home + cwd; this file records global choices shared
// by every session. Built-in tool names are filtered when each request header
// is built. PATCH writes then server.Reload() so catalogs rebuild.
// message.busy is steer (default) or queue and does not require Reload.
// mcp contains raw, case-sensitive server names, not canonical tool names.
// Workspace-scoped settings preserve disabled names outside their editable
// catalog, so one project cannot silently re-enable another project's servers.
// Version 2 canonicalizes tool names and migrates removed shell/agent/task
// names to a conservative union of replacement capabilities. Disabled wins.
// Version 3 removes code_mode overrides because Code Mode is always mixed.
// Schema version and downgrade rules: docs/state.md.
package toggles
