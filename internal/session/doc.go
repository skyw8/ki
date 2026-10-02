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
// turns (EnqueueSystem). Agent communication and completions instead use the
// context queue and never start idle turns. Completion input carries its stable
// task/generation identity. clientRequestId
// survives queue dispatch and promotion; versioned queues use internal/state.
// The server arbitrates completion ownership at actual persistence, not
// enqueue. Dequeue serves
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
// are foldable context, not turn boundaries or per-turn keep slots.
// Compaction rows are metadata:
// always kept visible and never counted toward the per-turn keep, so a
// trailing checkpoint cannot fold the newest reply.
// Start/end lifecycle entries remain in compact bodies even without a
// checkpoint: empty/failed outcomes must survive recovery. One paired status
// row (or its committed checkpoint) renders outside folds; lifecycle progress
// does not increment model steps. Later requests cannot finish an abandoned
// earlier start.
// Keep counts only ordinary assistants and distinct tool call IDs. HiddenCount
// counts every foldable node before the first kept or live real reply, including
// runtime user messages; later notices remain visible in chronological order.
// With no kept/live real reply, all foldable nodes hide, even runtime-only input.
// Its first body remains a stable turn anchor independent of row visibility.
// VisibleNodeIDs also includes compaction/cancellation metadata and aborted
// assistants, so consumers must classify those IDs before combining visible
// foldable nodes with HiddenCount.
// Sparse cross-turn edges require matching canonical parent/tail identities,
// not merely consecutive turn ordinals, which can also occur on sibling branches.
// Hidden reply bodies are transferred
// only on explicit turn expansion; its local
// cursor never replaces the main history cursor. Compact snapshots carry a
// per-turn entryCount (monotone within one selected branch), the latest assistant
// completion boundary, and
// only that assistant batch's tool states for sparse live reconciliation.
// Runtime-only compact history starts at turn 1. Turn clocks anchor on the
// first human (or first runtime user without a human), never later notifications.
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
// is append-only, body reads reuse appended bytes in a weighted LRU (64 MiB
// process-wide, 8 MiB per session, at most 256 sessions). Oversized reads are
// served without admission; eviction detaches ownership and never mutates
// in-flight slices. TailEntries/LeafTail trim older cached windows. OpenFrom
// builds a Session from an immutable read. Identical system prompts and tool
// schemas share storage within that body's pool, which expires with its owner.
// ReadTranscript separately caches body-free branch/turn metadata and exact
// file offsets (16 MiB, at most 256 sessions). Metadata retains accepted-input
// clientRequestId for replay coverage, including hidden messages.
// Lookup, historical pages and
// compact views hydrate selected bodies only, without promoting the body cache.
// Both caches revalidate file identity, size and mtime; replacements rebuild
// snapshots and stale offset reads fail. Identity is captured by handle Stat,
// not deferred path lookup, so size/mtime-preserving replacements invalidate
// even the first cached read on Windows. Handles close within each read and
// are never retained by caches. Runtime-only GETs skip transcript reads.
// A tail read from byte zero is complete even though the header is not an
// entry; an explicit branch root also ends paging without unrelated branches.
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
// Public slim, compact, exact-body and index projections never expose encrypted
// checkpoint items. Their derived remoteContext flag identifies remote
// checkpoints without claiming that the current provider can reuse them;
// this view-only flag is not persisted.
// Public entries/index rows also derive contextEstimate before shortening any
// bodies: UTF-8/4 estimates for system, tool schemas, message text/thinking/tool
// arguments and local summary text. Known zero differs from an omitted unknown
// component. Images, opaque signatures/checkpoints and provider usage are not
// converted into these textual estimates. The body-free scanner discards full
// content after estimating and caches only bounded schema digests/counts;
// estimates are never appended to stored entries or used for provider replay.
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
// Code Mode nested tool audits are non-message details entries. Their parent
// call and cell identity never create an unmatched provider toolResult.
package session
