# goal

Go extension that keeps a session-scoped objective active across turns until it
is complete, blocked, paused, cleared, or reaches its automatic continuation cap.
It uses the same command, tools, prompts, UI panels, and lifecycle protocol as the
original extension.

## Build and install

Build from the repository root using the root Go module:

```bash
go build -trimpath -o extensions/goal/bin/goal ./extensions/goal
# On Windows, use bin/goal.exe.
```

Distribute `extension.json`, `locales/`, and the platform executable in `bin/`
under `{KI_HOME}/extensions/goal`. The executable embeds `prompts.json` and
`tools.json`, so runtime installation needs no Go toolchain, source code, Bun,
Node, or external prompt/schema files. It speaks NDJSON JSON-RPC on stdin/stdout;
Ki supplies `KI_HOME` and session lifecycle events.

## Commands

| command | behavior |
|---|---|
| `/goal <objective>` | start a goal; an unfinished goal must be edited or cleared first |
| `/goal` or `/goal status` | show the current goal |
| `/goal edit <objective>` | replace the objective, invalidate older completion calls, reset continuation counters, and resume |
| `/goal pause` | pause and cancel any waiting deadline |
| `/goal resume` | resume a paused, blocked, or waiting goal and reset its automatic-turn counter |
| `/goal clear` or `/goal stop` | clear the goal |

Command objectives allow up to 4,000 UTF-16 code units. The form preserves the
objective's line breaks and spacing; the command normalizes token whitespace.
The status panel exposes the objective, state, counters, timestamps, and goal ID.
Clearing through the UI asks for confirmation.

## Continuation and waiting

Before each agent start, the active objective is appended to the system prompt.
When the agent settles, an active goal without a wait enqueues one continuation
with an idempotency marker. At 25 automatic turns, it pauses. Restoring an active
goal checks that the session is idle before enqueuing its next continuation.

`goal_complete`, `goal_blocked`, and `goal_wait` require the current `goal_id`.
Completion rejects summaries that say the work remains incomplete or tests fail.
Blocking requires a reason, evidence, and at least three repeated turns.

`goal_wait` keeps the goal active while suppressing automatic continuations. Its
optional `resume_after_ms` safety deadline accepts integers from 1 to
2,147,483,647; values below 10,000 are clamped to 10,000. Without a deadline,
waiting stays quiet until explicit resume. A deadline requests one continuation,
and a restored overdue deadline wakes once. Editing, pausing, completing,
blocking, clearing, or closing the session cancels its pending timer.

State lives at `{KI_HOME}/goal/<session-id>.json` and is written atomically with
version 1 and private permissions through `internal/state`. The extension also
appends a `goal-state` custom session entry after each change. Unsupported newer
state is never overwritten. Invalid optional waiting metadata does not discard
the rest of a restored goal.

## Tests

```bash
go test -race ./extensions/goal
```

Tests cover original command and prompt cases, initialization and lifecycle
system prompts, stale completion guards, blocked/wait validation, continuation
limits, persistence, session isolation, timer cancellation, restored waiting,
and exact whitespace/UTF-16 boundaries.

## Source package fallback

To stage a source package that can build outside this checkout, run from the repository root:

```bash
go run ./scripts/build-extensions.go -source -out var/extensions-source -only goal
```

Copy `var/extensions-source/goal` to `{KI_HOME}/extensions/goal`. When `bin/goal` (`.exe` on Windows) is missing, Ki runs `go run ./install/main.go` at the package root. The package includes a standalone Go module and minimal shared Ki sources; it excludes private configuration, state, caches, and build output. Go is required for this first build. It builds the native executable with CGO disabled. Once the binary exists, installation is skipped and no compiler or source files are needed to launch it. Default binary packages retain the same manifest metadata but omit sources and the installer; replace an incomplete binary package with a complete binary or source package.
