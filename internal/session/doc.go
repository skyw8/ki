// Package session is the append-only jsonl conversation tree.
//
// One session is one directory: events.jsonl + config.json. New rows always
// append; config.activeLeafId persists the selected branch across opens.
// SetLeaf moves the leaf without deleting old rows. ForkAt creates a new
// directory containing only the root-to-target path.
//
// request_header entries store system/tools plus provider, model, thinking,
// catalog, and pricing snapshots. context_usage entries store model-facing
// context pressure. config.json owns provider/model/thinking effort plus
// title, pin, and skills/mcp toggles. Remove deletes the session directory.
// List walks the session root. Index caches id→dir for O(1) lookup; the
// filesystem stays the source of truth and misses fall back to Find. On-disk layout: docs/session.md.
package session
