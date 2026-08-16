// Package prompt assembles the layered system prompt.
//
// After compaction the next prompt rebuilds the prefix (provider prompt
// cache is invalid). Layers: ki identity; one-line tool snippets (long
// CC prompts stay on tool definitions); skills XML if Read is present;
// AGENTS.md / CLAUDE.md from ~/.ki plus cwd up to the git root; cwd and
// local date/tz.
package prompt
