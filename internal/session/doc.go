// Package session is the append-only jsonl conversation tree.
//
// One session is one directory: events.jsonl + config.json. The leaf pointer
// is in memory only; new rows always append. On reload the last non-header
// line is the leaf. Revert moves the leaf; old rows are never deleted.
//
// request_header entries store the system+tools snapshot for a turn.
// config.json may override title, pin, and skills/mcp toggles. Remove deletes the session directory.
// List walks the session root. On-disk layout: docs/session.md.
package session
