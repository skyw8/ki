// Package prompt assembles the layered system prompt.
//
// Build is a pure renderer over a resources.Snapshot plus per-request runtime
// inputs. After compaction the next prompt rebuilds the prefix (provider prompt
// cache is invalid). Layers: ki identity and config layout; one-line tool
// snippets (long CC prompts stay on tool definitions); guidelines; the optional
// project/global appended system prompt; skills XML if Read is present;
// AGENTS.md / CLAUDE.md from {KI_HOME} plus cwd up to the git root; cached
// runtime OS/architecture, cwd, and local date/tz. Only tools and the skills
// toggle remain per-request inputs. See docs/system_prompt.md.
package prompt
