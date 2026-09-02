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
// Shell and other child processes receive Ki's inherited HTTP(S)/FTP/ALL proxy
// variables explicitly, so commands launched by them keep the same network
// routing. Runtime configuration remains authoritative for sidecar overrides.
// Agent delegates through a narrow AgentRuntime supplied by server. Its child
// session is created with session forkMode=tree and is bounded to three child
// layers below the main session; SendMessage steers or resumes the stable child
// task. TaskOutput and TaskStop use a composite task store so shell and agent
// tasks share the Claude Code-shaped lifecycle schema. File
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
// ripgrep binary, so an installed ki does not require rg in PATH.
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
