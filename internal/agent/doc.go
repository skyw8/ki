// Package agent owns stable root-scoped logical-agent identities and execution
// admission independently of terminal processes. Controller retains addresses
// across turns and releases execution capacity when work finishes. Root-scoped
// active child capacity defaults to four and includes waiting turns; there is
// no fixed depth limit. Live capacity never evicts live tasks.
//
// Messages accept context without starting idle work; explicit follow-ups admit
// or queue new generations. Interruption preserves identity and leaves separately
// owned processes running. Subtree cleanup fences admission before collecting
// descendants. Metadata and queued work use internal/state; restoration registers
// identities before scheduling pending work. Completion delivery uses a durable
// per-generation ledger, with no cross-file crash transaction.
//
// Progress is reduced from bounded ProgressEvents projected by the server, not
// loop events or inherited transcript scans. Revisions, current tools, latest
// context, per-run and lifetime usage remain generation-scoped; stale callbacks
// cannot update newer work. Old metadata marks lifetime statistics incomplete.
// Runtime is a host interface; this package does not import HTTP, providers,
// tool adapters or the loop. Cross-package contracts: docs/tools.md.
package agent
