// Package resources loads the immutable, cwd-bound resources used by one
// session: runtime environment metadata, AGENTS/CLAUDE context, skills, prompt
// templates, and merged MCP configuration. Loader is owned by a server and
// caches one atomic Snapshot per real session id; Scan serves non-session
// settings views without caching. Toggles and MCP connections are runtime state
// and are deliberately excluded.
package resources
