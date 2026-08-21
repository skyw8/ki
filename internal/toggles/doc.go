// Package toggles stores global skills/MCP enablement in {KI_HOME}/toggles.json.
//
// Discovery still uses home + cwd; this file only records disabled names
// for every session. PATCH writes then server.Reload() so catalogs rebuild.
package toggles
