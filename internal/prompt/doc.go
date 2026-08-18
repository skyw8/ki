// Package prompt assembles the layered system prompt.
//
// After compaction the next prompt rebuilds the prefix (provider prompt
// cache is invalid). Layers: ki identity; one-line tool snippets (long
// CC prompts stay on tool definitions); skills XML if Read is present;
// AGENTS.md / CLAUDE.md from ~/.ki plus cwd up to the git root; ki's own
// config layout (KI_HOME + ki.toml / .mcp.json / skills / server.json, the
// single-binary analogue of pi's docs section); cwd and local date/tz.
//
// Skills and AGENTS/CLAUDE context files are cached per session (home, cwd,
// session id), so a new session re-reads disk even in the same workspace
// while messages within one session hit the cache. Both are cleared by
// server.Server.Reload(), which runs after every successful compaction and
// is the future /reload command's hook.
package prompt
