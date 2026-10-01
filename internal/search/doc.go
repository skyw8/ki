// Package search provides the local Grep and Glob engines used by the
// model-facing tools. The engines invoke the embedded ripgrep binary directly
// with argv; no shell is involved. Output is parsed incrementally, bounded by
// result/output limits, and useful matches already read before a timeout are
// retained. EAGAIN/resource exhaustion retries once with a single ripgrep
// worker.
//
// On supported targets the package embeds rg and fd and materializes the
// available executables, plus a BASH_ENV shim, into one tools directory
// (ToolsDir). The shell tools prepend that directory to PATH so bundled tools
// take precedence over host installations, which keeps ki self-contained where
// binaries are available; unsupported targets may require a system rg and have
// no bundled fd. The shim also re-prepends extension-contributed directories
// from KI_EXTENSION_PATH_DIRS after login profiles run, so the shared shim file
// stays independent of the enabled extension set.
package search
