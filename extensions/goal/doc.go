// Command goal implements the session-scoped /goal command as a standalone
// NDJSON JSON-RPC executable. Prompt templates and tool schemas are embedded;
// launching a built binary never requires source code or an interpreter.
// Session mutations, continuation scheduling, and waiting deadlines use an
// ordered queue and mutex per session; UI/lifecycle notifications acknowledge
// immediately, including while another session awaits confirmation. State is
// versioned through internal/state; edits replace the stale-turn guard, while
// pause/resume retains the objective. Waiting keeps
// an active goal quiet until explicit resume or one safety-deadline wake. See
// README.md for commands, persistence, and the 25-turn continuation limit.
package main
