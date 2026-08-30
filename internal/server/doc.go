// Package server is the local HTTP backend. It orchestrates loop, session
// persist, tools, and providers. The same process serves the embedded WebUI.
//
// API auth is Bearer token or a browser session cookie, except GET /v1/health
// and the auth status/login endpoints. Browser login exchanges the bearer
// secret for an HttpOnly session cookie and a separate CSRF token; the token
// is never embedded in the SPA HTML. Unsafe browser requests must echo the
// CSRF token in X-Ki-CSRF. The CLI continues to use Bearer auth.
// Provider CRUD manages the offline registry and credentials; provider
// globally discovered extensions add process-level sidecar runtimes and read-only catalog entries;
// GET /v1/models
// is its flat selectable view. GET /v1/meta exposes the last-used model
// (or the first available fallback), that model's default thinking
// effort, and user home.
// GET /v1/extensions lists the global extension catalog, optional extension
// i18n resources, runtime status, and process-level extension UI projection.
// Workspaces live in {KI_HOME}/workspaces.json. Session cwd comes from a
// workspace (or a tmp+ workspace). GET /v1/sessions/{id} includes a
// read-only catalog (availableSkills / availableExtensions, including global
// extension i18n/UI, commands[]), session extensionUi, and runtime.ready. Opening a session (POST create, GET by id,
// fork) prepares the session view of already-running extensions in the
// background; List does not. runtime.ready is
// true when that Prepare finishes (failure still counts). PATCH /v1/sessions/{id} writes model /
// thinking / title / pin / leaf / queued. Skills and extension enablement is
// {KI_HOME}/toggles.json via GET/PATCH /v1/skills and /v1/extensions.
// Extension session.appendMessage accepts normal user messages without
// starting a run; busy sessions hold them in a durable context queue and
// dispatch drains them at the captured prompt boundary. Prompt accepts content blocks and an optional branch parent before assembling
// the model request. GET /v1/fs lists directories; files=1 also lists regular
// files; preview=1 streams authenticated image, plain-text/code, and PDF
// previews for the attachment picker. POST creates directories.
// Session attachment uploads are content-addressed under that session dir.
// request_header, context_usage, and streamed apply_patch preview events
// persist on jsonl/SSE.
// Non-/v1 paths serve the SPA. Unknown non-asset paths also serve index.html
// in place (do not 302 to "/": port-forwards would leave the page blank).
// The SPA shell contains no server secret; it establishes a browser session
// through the auth endpoints before calling the API. The UI is used behind
// port-forwards and on explicitly configured private listeners.
// A second prompt on a busy session steers or queues (toggles message.busy,
// overridable with delivery). queueId + delivery=steer takes that queued item
// into the captured run's Inbox. parentId while busy is 409. message_end awaits jsonl
// append; asynchronous extension lifecycle notifications are written in loop
// order, so message_end cannot be overtaken by agent_settled. agent_end may
// auto-compact. SSE replays runState.evs and drains after done.
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
package server
