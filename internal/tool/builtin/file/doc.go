// Package filetools implements model-aware file and search tools. Relative
// paths resolve against session cwd. File mutations share a per-path queue.
// Batch edit replaces non-overlapping spans against one original; apply_patch
// preflights all files before writing and records committed diffs, including
// line endings. Streamed freeform patch parsing emits non-executing previews.
// Read supports bounded paging, images and PDF for capable models. Grep/glob
// use embedded ripgrep, honor ignore rules by default and retain partial results.
// Model capability selection is supplied to Set.Build by builtin composition.
// Cross-package contracts: docs/tools.md.
package filetools
