// Package catalog owns built-in wire identifiers and the reserved name set.
// Tool adapters and family assembly use these identifiers; extension validation
// can inspect all reserved names without importing builtins or runtime services.
// Names include both mutually exclusive editor families and collaboration tools
// even when a model or runtime does not expose them.
package catalog
