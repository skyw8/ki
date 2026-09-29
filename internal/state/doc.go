// Package state owns the on-disk contract for Ki's versioned JSON documents
// under {KI_HOME}: models.json, credentials.json, workspaces.json,
// toggles.json, and push-subscriptions.json. It exists so every store applies
// the same schema rules instead of each inventing its own.
//
// A document carries a top-level integer "version". Loading walks forward
// migrations in memory; a document whose version is newer than the running
// build is never coerced down (that would drop fields the old build cannot
// see) and instead yields ErrNewerVersion. Writes are atomic and refuse to
// overwrite a newer document, so an old build can never clobber state it does
// not understand. See docs/state.md.
package state
