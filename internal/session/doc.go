// Package session is the append-only jsonl conversation tree.
//
// One session is one directory: events.jsonl + config.json, plus queue.json
// for turns waiting on the current run, ext-queue.json for extension FIFO
// prompts, and context-queue.json for normal user messages that must enter a
// later prompt without starting a run. config.json and the queue JSON files
// are replaced atomically (temp + rename) so concurrent readers never observe
// truncated JSON. A per-directory file gate serializes Open against jsonl
// appends across distinct Session handles, and an append holds its descriptor
// only for the write itself: a Session keeps no open events.jsonl, so a session
// directory stays deletable while other goroutines or processes use it (POSIX
// allows that, Windows only while no handle is open). New
// rows always append; config.activeLeafId persists the selected branch across opens.
// The main queue holds two lanes: human turns (Enqueue) and server-generated
// turns such as agent completion notifications (EnqueueSystem; a notification
// also records the agent task it reports through
// EnqueueAgentNotification, which lets dispatch drop it when the parent has
// already read that result). Dequeue serves
// the oldest human turn first and FIFO within a lane, so a person waiting on a
// reply never sits behind a system message.
// SetLeaf moves the leaf without deleting old rows. ForkAt creates a new
// directory containing only the root-to-target path and records the parent and
// fork mode in the header. The server owns tree-mode cascade deletion.
//
// request_header entries store system/tools plus provider, model, thinking,
// catalog, and pricing snapshots. WebUI GET projects a slimmed active-leaf tail
// (unchanged prompts omitted, large bodies truncated) and, on request, a
// body-less index of the whole tree. Because the jsonl is append-only, reads
// are served from a per-directory cache that decodes only the bytes appended
// since the last read: TailEntries (LeafTail) reads just the end of the file,
// AllEntries extends the same cache to the whole transcript, and OpenFrom
// builds a Session from entries a caller already took from that cache.
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
