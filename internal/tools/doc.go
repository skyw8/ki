// Package tools implements model-aware built-ins: Read, Write, Edit,
// apply_patch, Grep, Glob, Bash, PowerShell, Agent, SendMessage, TaskOutput,
// TaskStop, and Monitor.
//
// Wire names and input schemas follow Claude Code. Text results follow pi
// (no cat -n; shell tools mix stdout/stderr; non-zero exit is an error). Relative
// paths resolve against the session cwd. Each shell call is a new process from
// that cwd, so cd and Set-Location are not remembered. Windows probes configured
// paths, standard Git installations, then PATH for Bash, and additionally
// exposes PowerShell, preferring pwsh over powershell.exe. Other platforms probe
// /bin/bash then PATH. When Bash is unavailable, Bash and Monitor are omitted
// without preventing server startup. The session-scoped task store tracks
// process groups, output files, status, exit code, cancellation, and progress.
// Its complete output files are created through the tool-output store
// (OutputSpool), so they live in the session spill directory and are removed
// with it; a process temporary file is the fallback when the store refuses.
// Shell and other child processes receive Ki's inherited HTTP(S)/FTP/ALL proxy
// variables explicitly, so commands launched by them keep the same network
// routing. Runtime configuration remains authoritative for sidecar overrides.
// Agent delegates through a narrow AgentRuntime supplied by server. Its child
// session is linked with forkMode=tree and, by default (inherit_context), is
// seeded with the parent's finished history up to the user message that
// triggered the in-flight turn; inherit_context:false starts it clean. The
// directive itself arrives as the child's first user message wrapped in a
// subagent envelope carrying its depth, the session that delegated, and — at
// MaxAgentDepth — the instruction not to delegate again, so the child's system
// prompt and tool schemas can stay byte-identical to its parent's. Set therefore
// never withholds Agent; the depth limit is refused by the spawn call, not by
// trimming the tool set (which would break the cached prefix). It is
// bounded to three child layers below the main session. SendMessage addresses a
// child by its stable task id, or resolves the reserved "parent"/"main"
// addresses from the sender's session chain so a subagent can reach its caller.
// A child run is
// detached from its caller, and a foreground Agent call that exceeds its
// two-minute wait is promoted to a background task instead of being cancelled
// (the caller gets the run_in_background async_launched shape, and its turn
// keeps running: only an explicit run_in_background terminates it, and every
// async result carries a note telling the caller where the completion lands).
// A completion notification is delivered mid-turn when the parent run is still
// alive and through the durable queue otherwise, and TaskOutput/TaskStop mark a
// run whose result the caller already has so it is not reported twice.
// TaskOutput and TaskStop use a composite task store so shell and agent tasks
// share the Claude Code-shaped lifecycle schema. File
// mutations share a server-scoped per-path queue; Edit additionally
// supports non-overlapping batch replacements against one original. Structured
// result details are persisted for clients but omitted by provider adapters.
// Read exposes line and UTF-8-safe byte paging, injectable operations, and
// bounded image processing. Foreground shell results keep only a bounded,
// ANSI-free tail in model context and point
// truncated results at the complete session-scoped temporary output file.
// A foreground timeout promotes a still-running command to a background task;
// explicit background tasks can be inspected by bounded TaskOutput or stopped by
// TaskStop. Monitor streams Bash output through ToolExecutionUpdate. Search
// tools use the same process-tree termination contract and run an embedded
// ripgrep binary, so an installed ki does not require rg in PATH. ki also
// embeds fd; both are materialized into one tools directory that Bash and
// PowerShell prepend to PATH, and Bash additionally sources a BASH_ENV shim so
// rg/fd stay resolvable even after a login profile rewrites PATH. Enabled
// extensions can contribute their own PATH directories (Set.PathDirs, from the
// session's resource snapshot); those follow the bundled directory and are
// re-prepended by the same shim from KI_EXTENSION_PATH_DIRS.
// Set.Build selects a text/rich Read and exactly one editor family from the
// provider-neutral Profile. apply_patch uses the Codex freeform patch grammar,
// verifies the complete patch before its first write, preserves mixed line
// endings, tracks the definitely committed prefix on failure, and exposes
// throttled argument previews without placing structured details in model
// context. The server applies FilterBuiltins with the global tools toggle
// before appending extension tools.
//
// Parameter and result tables: docs/tools.md.
package tools
