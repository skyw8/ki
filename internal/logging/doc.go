// Package logging configures the process logger: JSONL slog to stderr and
// {KI_HOME}/ki.log. The file is size-rotated with a cross-process lock.
//
// Do not log API keys, bearer tokens, or file bodies. Token usage and tool
// failures belong in session jsonl, not here. Recover records panic values and
// stacks at process, HTTP, and background-task boundaries. KI_DEBUG=1 forces
// debug.
package logging
