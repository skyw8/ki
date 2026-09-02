// Package toggles stores global built-in tool, skill, and extension enablement
// plus the busy-message delivery default in {KI_HOME}/toggles.json.
//
// Discovery still uses home + cwd; this file only records disabled names
// for every session. Built-in tool names are filtered when each request header
// is built. PATCH writes then server.Reload() so catalogs rebuild.
// message.busy is steer (default) or queue and does not require Reload.
package toggles
