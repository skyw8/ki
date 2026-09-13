// Package session is the append-only jsonl conversation tree.
//
// One session is one directory: events.jsonl + config.json, plus queue.json
// for turns waiting on the current run, ext-queue.json for extension FIFO
// prompts, and context-queue.json for normal user messages that must enter a
// later prompt without starting a run. config.json and the queue JSON files
// are replaced atomically (temp + rename) so concurrent readers never observe
// truncated JSON. A per-directory file gate serializes Open against jsonl
// appends across distinct Session handles. New
// rows always append; config.activeLeafId persists the selected branch across opens.
// The main queue holds two lanes: human turns (Enqueue) and server-generated
// turns such as agent completion notifications (EnqueueSystem). Dequeue serves
// the oldest human turn first and FIFO within a lane, so a person waiting on a
// reply never sits behind a system message.
// SetLeaf moves the leaf without deleting old rows. ForkAt creates a new
// directory containing only the root-to-target path and records the parent and
// fork mode in the header. The server owns tree-mode cascade deletion.
//
// request_header entries store system/tools plus provider, model, thinking,
// catalog, and pricing snapshots. WebUI GET projects a body-less index plus a
// slimmed active-leaf tail (unchanged prompts omitted, large bodies truncated).
// context_usage entries store model-facing
// context pressure; patch_apply_updated entries store non-executing structured
// patch previews. Asynchronous sideband rows never advance activeLeafId.
// config.json owns provider/model/thinking effort plus
// title and pin. Skills/extension enablement is process-wide ({KI_HOME}/toggles.json).
// Remove deletes the session directory.
// CreateChild makes a clean delegated-agent session: it records the same parent
// edge and forkMode=tree but copies no transcript, so a subagent only sees the
// directive it was given. A child that inherits context instead forks at
// LastUserBoundary, which stops before the user message that triggered the
// parent's in-flight turn so that message is never mistaken for the child's
// task. Its transcript, relationship, and agent.json metadata
// remain durable so the server can rebuild an agent task after restart.
// Removing a child also removes its agent record through the server-owned
// lifecycle.
// List walks the session root with a shallow scan: each row reads config.json,
// the jsonl header, and the first user message only, so listing never decodes a
// full transcript. ListCache reuses rows while config.json and events.jsonl keep
// their size and mtime. Index caches id→dir for O(1) lookup; the filesystem
// stays the source of truth and misses fall back to Find. On-disk layout: docs/session.md.
package session
