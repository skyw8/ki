// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. The same process serves the embedded WebUI.
//
// API auth is Bearer token or a browser session cookie, except GET /v1/health
// and the auth status/login endpoints. Browser login exchanges the bearer
// secret for an HttpOnly session cookie and a separate CSRF token; the token
// is never embedded in the SPA HTML. Unsafe browser requests must echo the
// CSRF token in X-Ki-CSRF. The CLI continues to use Bearer auth. A browser
// session expires after 12h idle and, once more than half that window has
// elapsed, an authenticated request rewrites both cookies to extend it, so a
// tab left in use stays signed in while an abandoned one still ages out.
// Provider CRUD manages the offline registry and credentials; provider
// globally discovered extensions add process-level sidecar runtimes and read-only catalog entries;
// GET /v1/models
// is its flat selectable view. GET /v1/meta exposes the last-used model
// (or the first available fallback), that model's default thinking
// effort, and user home.
// GET /v1/commands exposes the workspace-scoped built-in, prompt-template,
// and skill catalog used by the WebUI before a session exists.
// GET /v1/events is the WebUI push channel: one SSE stream per browser tab
// carrying invalidate frames (scope sessions/workspaces/providers/extensions:
// "this changed, refetch it through the ordinary REST endpoint") and session
// sideband loop events tagged with sessionId (agent_end, run_aborted,
// runtime_ready, extension notices/UI, manual compaction, queue changes). It
// replaced the per-running-session notification stream, so one tab holds one
// push connection instead of one per running session. Nothing is replayed: the
// ready frame is the client's cue to refetch, which is also how a reconnect
// catches up. The pushed agent_end carries no messages; the run's full event
// log is replayed only to the client holding that run's SSE.
// GET /v1/extensions lists the global extension catalog, optional extension
// i18n resources, runtime status, and process-level extension UI projection.
// Web Push (docs/push.md) is served by GET /v1/push/config (the VAPID public
// key), POST /v1/push/subscriptions (register a browser endpoint; upsert, so the
// client re-syncs on every load) and DELETE /v1/push/subscriptions. Every
// finished run also queues a push for those endpoints; the service encrypts per
// subscription (RFC 8291) and prunes one the push service reports gone (404/410).
// It is best-effort and never blocks the run's event funnel.
// Workspaces live in {KI_HOME}/workspaces.json. Session cwd comes from a
// workspace (or a tmp+ workspace). GET /v1/sessions/{id} returns a WebUI
// view: the newest entries of the active leaf (unchanged request_header
// system/tools omitted; large bodies truncated) plus a read-only catalog
// (availableSkills / availableExtensions, including loaded
// skills/tools/commands/promptAppend/providers and global extension i18n/UI,
// commands[]), session extensionUi, and runtime.ready.
// The same route's view=trace and view=inspect projections expose bounded
// active-leaf diagnostics to CLI clients without preparing extension runtime;
// unsupported view names are rejected rather than treated as detailed.
// The tree index is opt-in (fields=index, only id/index, content ETag): it needs the whole transcript, and
// carrying it on open made a long session wait for a full parse and megabytes
// of JSON that the newest messages did not need. A session smaller than one
// tail read, which is read in full anyway, still answers with the index so the
// WebUI needs no second request. fields=runtime omits the transcript
// entirely; fields=index,runtime includes tail/index/runtime together.
// entry/entries fetch full bodies; before+limit pages older leaf
// entries backwards from oldestId. The default tail is a byte window of the
// transcript (LeafTail), not the whole branch, so hasMore stays true until a
// read or page actually reaches the branch root; a cursor outside the active
// branch yields 409, not an empty root page. The same applies to an expansion
// cursor outside its requested turn. A valid root cursor returns an empty page
// with hasMore=false. messages is not included. Opening a session (POST create, GET by id,
// fork) prepares the session view of already-running extensions in the
// background; List does not. runtime.ready is
// true when that Prepare finishes (failure still counts). PATCH /v1/sessions/{id} writes model /
// thinking / title / pin / leaf / queued. Built-in tool, skill, and extension
// enablement is {KI_HOME}/toggles.json via GET/PATCH /v1/tools, /v1/skills,
// and /v1/extensions. The built-in tool catalog includes model-specific editors
// even when unavailable to the selected model, with an available marker, so a
// global toggle survives model switches.
// GET /v1/prompt/append lists every appended-system-prompt source (the
// read-only built-in layer, the editable {KI_HOME} and workspace files, the
// read-only extension layers) with the effective append stack; PUT/DELETE edit
// one source file by name (never by client-supplied path) and reload sessions
// so the next prompt reads it.
// Extension session.appendMessage accepts normal user messages without
// starting a run; busy sessions hold them in a durable context queue and
// dispatch drains them at the captured prompt boundary. Prompt accepts content blocks and an optional branch parent before assembling
// the model request. GET /v1/fs lists directories; files=1 also lists regular
// files; preview=1 streams authenticated image, plain-text/code, and PDF
// previews for the attachment picker. POST creates directories.
// Session attachment uploads are content-addressed under that session dir.
// request_header and context_usage persist on jsonl/SSE.
// Non-/v1 paths serve the SPA. Unknown non-asset paths also serve index.html
// in place (do not 302 to "/": port-forwards would leave the page blank).
// Every text response (SPA assets and /v1 JSON) is gzipped except
// text/event-stream, which is flushed per event; assets/ is content-hashed and
// sent immutable while the SPA HTML stays no-store.
// The SPA shell contains no server secret; it establishes a browser session
// through the auth endpoints before calling the API. The UI is used behind
// port-forwards and on explicitly configured private listeners.
// A second prompt on a busy session steers or queues (toggles message.busy,
// overridable with delivery). queueId + delivery=steer takes that queued item
// into the captured run's Inbox. parentId while busy is 409. message_end awaits jsonl
// append; asynchronous extension lifecycle notifications are written in loop
// order, so message_end cannot be overtaken by agent_settled. One run owns one
// event funnel (emit.go, runEmitter) that applies every loop event to jsonl,
// the run SSE buffer, the WebUI push stream, and extension lifecycle
// subscribers in that fixed order on the loop's goroutine. agent_end may
// auto-compact locally or through the standalone provider capability; models
// with server-side Responses compaction instead promote returned opaque items
// to a binding-scoped session checkpoint at message_end. Message-visible
// lifecycle hooks force portable replay and suppress durable server checkpoints
// because they cannot inspect an encrypted prefix. A steer accepted into
// the Inbox but never drained because the
// run was aborted is committed to jsonl as an unanswered user turn; a subagent
// completion notification uses the same Inbox while the parent run is live, so
// it lands inside the turn that started the agent, and falls back to the durable
// queue (system lane, tagged with its agent task) once that turn has ended.
// Dispatch drops a queued notification whose task result the parent already read
// or stopped. SSE
// replays runState.evs and drains after done, emitting a `: ping` comment every
// 15s while idle so a mobile or NAT path does not drop a connection that is
// silent for a whole model round; GET /v1/events heartbeats the same way, and
// SSE clients ignore comments. The buffered log is trimmed where
// a payload cannot help a reader that attaches later: a message's start and
// chunks leave the log once its message_end is persisted, superseded partials
// are blanked once every attached reader has passed them — or, for a reader
// that stopped consuming, once they are older than the retention caps in
// runState.trimLocked (a stalled reader used to pin 286 MB of repeated partials
// and made every re-attach replay all of it) — and a repeated request_header
// is stored as promptUnchanged without its body. Each event carries a per-run
// seq, sent as the SSE id line; a client may resume with Last-Event-ID or
// ?since=<runId>:<seq> instead of replaying the whole run.
// Each SSE reader encodes message_update as an initial full snapshot followed
// by smaller field patches. A reconnect gets a fresh snapshot regardless of
// trimming; canonical buffered events and lifecycle delivery are unchanged.
// Retained payload size is computed once per append, including tool arguments
// and raw fields. Both SSE endpoints disable proxy buffering and bound each
// write/flush to 30s; cancellation interrupts blocked supported writers. A
// failed heartbeat cancels the reader wait, and compression preserves flush
// errors. These per-write limits do not cap the lifetime of a healthy run.
//
// One server-owned resources.Loader atomically caches runtime environment,
// skills, AGENTS/CLAUDE, prompt templates, and discovered extension descriptors
// by session id. Settings scans are uncached. Session reload closes only that
// session's extension view; global settings reload idle sessions and queues
// active ones until occupy's matching release (prompt and compact).
// Agent tool calls fork tree-mode child sessions and run them through their own
// runState, bounded to three child layers below the main session. Agent metadata
// beside the child transcript rebuilds the stable task registry after restart;
// SendMessage steers a live Inbox or resumes the same child session, while
// TaskOutput/TaskStop expose the shared task lifecycle.
// Shutdown sets runtimeClosed so occupy and queue dispatch refuse new runs, then
// drains active runs until idle (release can otherwise chain a late dispatch).
//
// Routes and run lifecycle: docs/architecture.md.
// Compact session GETs project complete turns without hidden reply bodies;
// turn expansion reuses that GET with a turn-local cursor. Runtime and index
// queries remain independent of the browser display preference.
// Compact SSE readers name their persisted snapshot leaf to suppress covered
// reply bodies while retaining unfinished tools and newer messages.
package server
