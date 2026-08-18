// Package skills discovers SKILL.md packages for the system prompt XML.
//
// Search union: {KI_HOME}/skills, ~/.agents/skills, <cwd>/.ki/skills,
// and ancestor .agents/skills up to the git root. Each package is a
// top-level dir (symlinks followed) containing SKILL.md — the rest of
// the package is not walked. Session Toggle applies only/disabled.
// List skips the toggle so the config UI can show disabled items.
// Discover caches the unfiltered table per session (home, cwd, session id):
// a new session re-scans disk even in the same workspace while messages
// within one session hit the cache. Invalidate / InvalidateAll re-scan on
// demand; server.Reload() runs after compaction and is the future /reload
// command's hook. The model loads a skill by Read-ing its file path.
package skills
