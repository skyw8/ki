// Package prompt assembles the layered system prompt.
//
// Build is a pure renderer over a resources.Snapshot plus per-request runtime
// inputs. After compaction the next prompt rebuilds the prefix (provider prompt
// cache is invalid). Layers: ki identity; one-line tool snippets (long
// CC prompts stay on tool definitions); skills XML if Read is present;
// AGENTS.md / CLAUDE.md from ~/.ki plus cwd up to the git root; ki's own
// config layout (KI_HOME + ki.toml / .mcp.json / skills / server.json, the
// single-binary analogue of pi's docs section); cached runtime OS/architecture,
// cwd, and local date/tz. Only tools and toggles remain per-request inputs.
package prompt
