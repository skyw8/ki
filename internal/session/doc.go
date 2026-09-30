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
// request_header entries store system/tools (including custom type/grammar)
// plus provider, model, thinking, catalog, and pricing snapshots. WebUI GET projects a slimmed active-leaf tail
// (unchanged prompts omitted, large bodies truncated) and, on request, a
// body-less index of the whole tree. Views have a 512 KiB serialized slim-entry
// page budget as well as a count limit. Compact views instead page whole human
// turns: visible input/reply bodies plus a fold summary and canonical range
// metadata. Runtime-authored user-role messages (including subagent traffic)
// are foldable replies, not turn boundaries. Compaction rows are metadata:
// always kept visible and never counted toward the per-turn keep, so a
// trailing checkpoint cannot fold the newest reply. HiddenCount counts nodes,
// not entries: assistant messages, distinct tool call IDs,
// and runtime user messages. VisibleNodeIDs also includes always-visible
// compaction/cancellation metadata and aborted assistants, so consumers must
// classify those IDs before combining visible replies with HiddenCount.
// Sparse cross-turn edges require matching canonical parent/tail identities,
// not merely consecutive turn ordinals, which can also occur on sibling branches.
// Hidden reply bodies are transferred
// only on explicit turn expansion; its local
// cursor never replaces the main history cursor. Compact snapshots carry a
// per-turn entryCount (monotone within one selected branch), the latest assistant
// completion boundary, and
// only that assistant batch's tool states for sparse live reconciliation.
// Turn elapsed spans include assistant, tool-result and compaction completion;
// cumulativeElapsedMs includes earlier turns outside the loaded compact page.
// Detailed views keep count pagination. Trace projects the active leaf into
// stable diagnostic rows and Analyze aggregates cache misses, tool failures,
// prompt fingerprints, token usage and context pressure. Both share the
// compact view's cache classifier and reset its baseline at compaction. Search
// returns the first active-leaf text match per session; these read APIs back
// both the session CLI and HTTP projections.
// Oversized entries retain identity/statistics and advertise truncation; full
// bodies stay available through entry/entries. A page's cursor is its actual
// contiguous boundary, not its additional turn-opening user entry. Since jsonl
// is append-only, reads use a per-directory cache that decodes only bytes appended
// since the last read: TailEntries (LeafTail) reads just the end of the file,
// AllEntries extends the same cache to the whole transcript, and OpenFrom
// builds a Session from entries a caller already took from that cache. A tail
// read from byte zero is complete even though the header is not an entry; an
// explicit branch root also ends paging without reading unrelated branches.
// context_usage entries store model-facing context pressure;
// patch_apply_updated stores non-executing structured patch previews.
// A local compaction stores a portable summary plus retained tail. A remote
// Responses compaction instead stores a canonical raw-item window bound to
// provider/API/base/model/credential/compaction protocol. ContextToLeaf replays
// it only for an exact binding;
// MessagesToLeaf ignores remote checkpoints and remains the portable
// projection used for model switches and local fallback. A server-compacted
// assistant and checkpoint commit under one file gate with tail rollback.
// Prepared standalone/local compactions re-read config.activeLeafId under that
// same gate before append, so a second Session handle cannot commit a stale cut.
// Slim views never expose encrypted checkpoint items.
// Asynchronous sideband rows never advance activeLeafId. A run cancellation is
// published live immediately, then committed after terminal output as a normal
// non-message leaf so branch history renders it in order without model replay.
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
