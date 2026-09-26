// Package resources loads the immutable, cwd-bound resources used by one
// session: runtime environment metadata, AGENTS/CLAUDE context, the additive
// global and project appended system prompt files, extension prompt layers,
// skills, prompt templates, and discovered extension descriptors.
// AppendSystemPromptPath resolves those two supplement paths for both the loader
// and the settings writer, so reads and writes cannot drift apart.
// Loader is owned by a server and caches one atomic Snapshot per real session
// id; Scan serves non-session settings views without caching. Live sidecar
// clients are runtime state and are deliberately excluded.
package resources
