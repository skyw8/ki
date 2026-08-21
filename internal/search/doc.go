// Package search provides the local Grep and Glob engines used by the
// model-facing tools. The engines invoke the embedded ripgrep binary directly
// with argv; no shell is involved.
package search
