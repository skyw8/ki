// Package session is the append-only jsonl conversation tree.
//
// One session is one directory: events.jsonl + config.json. The leaf pointer
// is in memory only; new rows always append. On reload the last non-header
// line is the leaf. Revert moves the leaf; old rows are never deleted.
//
// List walks the session root. On-disk layout: docs/session.md.
package session
