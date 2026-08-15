// Package klog sets the process logger: slog to stderr and {KI_HOME}/ki.log.
//
// Do not log API keys, bearer tokens, or file bodies. Token usage and tool
// failures belong in session jsonl, not here. KI_DEBUG=1 forces debug.
package klog
