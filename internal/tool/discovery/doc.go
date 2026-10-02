// Package discovery publishes a bounded, searchable view of an immutable
// allowed tool catalog. Search uses lexical BM25 metadata, not embeddings.
// search_tool is a normal function tool on every provider API; it is never a
// nested JavaScript capability. Search discloses complete selected schemas but
// does not grant execution rights or mutate the worker's allowed tool snapshot.
//
// Successful paired search results retain only canonical discovered_tools IDs
// in Details. Loaded reconciles these IDs with the current catalog instead of
// trusting persisted schemas. Each identity additionally requires a complete
// matching schema in the actual post-hook body; redacted, stale, or truncated
// bodies cannot load declarations. Error, orphaned, and replayed results do not
// load tools. Schema outputs are capped below the ordinary tool preview budget;
// oversized schemas are omitted explicitly, never partially published.
package discovery
