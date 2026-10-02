// Package catalog owns built-in wire identifiers and the reserved name set.
// Tool adapters and family assembly use these identifiers; extension validation
// can inspect all reserved names without importing builtins or runtime services.
// Names include both mutually exclusive editor families and collaboration tools
// even when a model or runtime does not expose them.
// Exec and Wait remain reserved even while Code Mode is disabled, so extension
// registration cannot take an identifier that a later occupy may enable.
// search_tool is reserved and globally toggleable; its absence disables MCP
// deferral rather than leaving an undiscoverable model-facing catalog.
package catalog
