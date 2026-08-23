// Package tools implements model-aware built-ins: Read, Write, Edit,
// apply_patch, Grep, Glob, Bash, PowerShell, TaskOutput, TaskStop, and Monitor.
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
// Foreground shell results keep only a bounded tail in model context and point
// truncated results at the complete session-scoped temporary output file.
// A foreground timeout promotes a still-running command to a background task;
// explicit background tasks can be inspected by TaskOutput or stopped by
// TaskStop. Monitor streams Bash output through ToolExecutionUpdate. Search
// tools use the same process-tree termination contract and run an embedded
// ripgrep binary, so an installed ki does not require rg in PATH.
// Set.Build selects a text/rich Read and exactly one editor family from the
// provider-neutral Profile. apply_patch uses the Codex freeform patch grammar.
//
// Parameter and result tables: docs/tools.md.
package tools
