// Package logging configures the process logger: JSONL slog to stderr and
// {KI_HOME}/ki.jsonl. The file is size-rotated with a cross-process lock.
//
// Do not log API keys, bearer tokens, or file bodies. Conversation facts belong
// in session jsonl; privacy-safe cache/tool harness diagnostics use the
// session-local OTLP telemetry file. Recover records panic values and stacks at
// process, HTTP, and background-task boundaries. KI_DEBUG=1 forces debug.
package logging
