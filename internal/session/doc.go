// Package session is the append-only jsonl conversation tree.
//
// One session is one directory: events.jsonl + config.json, plus queue.json
// for user turns waiting on the current run and context-queue.json for normal
// user messages that must enter a later prompt without starting a run. New rows always
// append; config.activeLeafId persists the selected branch across opens.
// SetLeaf moves the leaf without deleting old rows. ForkAt creates a new
// directory containing only the root-to-target path and records the parent and
// fork mode in the header. The server owns tree-mode cascade deletion.
//
// request_header entries store system/tools plus provider, model, thinking,
// catalog, and pricing snapshots. context_usage entries store model-facing
// context pressure; patch_apply_updated entries store non-executing structured
// patch previews. Asynchronous sideband rows never advance activeLeafId.
// config.json owns provider/model/thinking effort plus
// title and pin. Skills/extension enablement is process-wide ({KI_HOME}/toggles.json).
// Remove deletes the session directory.
// Agent delegation uses the same ForkAt primitive with forkMode=tree; the child
// transcript, relationship, and agent.json metadata remain durable so the
// server can rebuild an agent task after restart. Removing a child also removes
// its agent record through the server-owned lifecycle.
// List walks the session root. Index caches id→dir for O(1) lookup; the
// filesystem stays the source of truth and misses fall back to Find. On-disk layout: docs/session.md.
package session
