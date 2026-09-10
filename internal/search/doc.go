// Package search provides the local Grep and Glob engines used by the
// model-facing tools. The engines invoke the embedded ripgrep binary directly
// with argv; no shell is involved. Output is parsed incrementally, bounded by
// result/output limits, and useful matches already read before a timeout are
// retained. EAGAIN/resource exhaustion retries once with a single ripgrep
// worker.
//
// The package also embeds fd and materializes both executables, plus a BASH_ENV
// shim, into one tools directory (ToolsDir). The shell tools prepend that
// directory to PATH so rg and fd work inside Bash no matter what the host has
// installed, which keeps ki a single self-contained binary.
package search
