// Package session is the append-only jsonl conversation tree.
//
// One session is one directory: events.jsonl + config.json. The leaf pointer
// is in memory only; new rows always append. On reload the last non-header
// line is the leaf. Revert moves the leaf; old rows are never deleted.
//
// On-disk layout and entry types: docs/session.md.
package session
