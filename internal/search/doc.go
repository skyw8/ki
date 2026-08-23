// Package search provides the local Grep and Glob engines used by the
// model-facing tools. The engines invoke the embedded ripgrep binary directly
// with argv; no shell is involved. Output is parsed incrementally, bounded by
// result/output limits, and useful matches already read before a timeout are
// retained. EAGAIN/resource exhaustion retries once with a single ripgrep
// worker.
package search
