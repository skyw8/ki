// Package tools implements model-aware file/search tools, terminal interaction,
// and asynchronous logical-agent coordination. Default wire names are snake_case;
// internal/toolname provides PascalCase aliases for one canonical schema.
//
// Set.Build selects text/rich read and one editor family: GPT Responses uses
// grammar-backed apply_patch, while other models use write/edit. Relative paths
// resolve against session cwd. File mutations share a per-path queue. Batch edit
// replaces non-overlapping spans against one original; apply_patch preflights all
// files before writing and records committed diffs, including line endings.
// Read supports bounded paging, images and PDF for capable models. Grep/glob use
// embedded ripgrep, honor ignore rules by default and retain partial results.
//
// exec_command starts a process owned by a session ShellProcessManager, then
// observes it for a bounded yield. write_stdin collects incremental output or
// writes PTY input. Unix PTY and Windows ConPTY support interactive terminals;
// pipes reject ordinary stdin but accept explicit interrupt requests. Canceling
// an observation never kills a process. Explicit process/tree stop, session
// deletion and shutdown own termination. Subtree cleanup fences new admission
// before collecting descendants, then stops runners before their processes. Live capacity never evicts live tasks.
// Raw stdout/stderr is spooled; memory, model output and live previews are bounded.
// Complete logs use OutputSpool with a manager-owned temporary-file fallback.
// Handles are numeric, session-scoped and cannot be restored after restart.
// Windows defaults to PowerShell then Git Bash; Unix defaults to discovered Bash.
// Proxy variables and bundled rg/fd plus extension PATH directories are inherited.
// Bash uses a BASH_ENV shim after login profiles to retain these directories.
//
// AgentController owns stable root-scoped /root/task identities independently
// of processes. spawn_agent returns immediately. fork_turns selects all, none,
// or N complete finished user turns. There is no fixed depth limit; root-scoped
// active child capacity defaults to four and includes waiting turns. Completed
// identities retain their address while releasing execution capacity.
// send_message queues context without starting idle turns. followup_task admits
// explicit work, retaining stable IDs while busy or capacity-limited. wait_agent
// observes mailbox/user input; list_agents and wait_agent do not consume results.
// interrupt_agent preserves identity and leaves owned processes running. Agent
// metadata and queued tasks use internal/state; restoration registers identities
// before scheduling pending work. Completion delivery uses a per-generation
// ledger at parent persistence, with no cross-file crash transaction. Progress
// is reduced from generation-bound run events with monotonic revision, phase,
// bounded current tools, latest context size, per-run and lifetime usage. Queued
// capacity and mailbox waiting are explicit; stale callbacks cannot update a
// newer generation. Old metadata marks lifetime statistics as incomplete.
//
// AgentRuntime is supplied by server; tools do not depend on HTTP or providers.
// The server applies global tool toggles before appending extension tools.
// Parameters, ownership and state migration: docs/tools.md.
package tools
