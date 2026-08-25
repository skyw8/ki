// Package toggles stores global skills/MCP/extension enablement and the busy-message
// delivery default in {KI_HOME}/toggles.json.
//
// Discovery still uses home + cwd; this file only records disabled names
// for every session. PATCH writes then server.Reload() so catalogs rebuild.
// message.busy is steer (default) or queue and does not require Reload.
package toggles
