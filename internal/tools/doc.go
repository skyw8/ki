// Package tools implements model-aware built-ins: Read, Write, Edit,
// apply_patch, and Bash.
//
// Wire names and input schemas follow Claude Code. Text results follow pi
// (no cat -n; Bash mixes stdout/stderr; non-zero exit is an error). Relative
// paths resolve against the session cwd. Each Bash is a new process from that
// cwd. run_in_background returns a task id and output_file for a later Read.
// Set.Build selects a text/rich Read and exactly one editor family from the
// provider-neutral Profile. apply_patch uses the Codex freeform patch grammar.
//
// Parameter and result tables: docs/tools.md.
package tools
