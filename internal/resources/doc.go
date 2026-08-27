// Package resources loads the immutable, cwd-bound resources used by one
// session: runtime environment metadata, AGENTS/CLAUDE context, the optional
// project/global appended system prompt, extension prompt layers, skills,
// prompt templates, and discovered extension descriptors.
// Loader is owned by a server and caches one atomic Snapshot per real session
// id; Scan serves non-session settings views without caching. Live sidecar
// clients are runtime state and are deliberately excluded.
package resources
