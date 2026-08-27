// Package command parses slash input and lists/expands commands.
//
// Builtins (new, cwd, compact, reload) are dispatched by the server. Skill and prompt
// templates expand to user text. KindExtension is a sidecar slash handler. Catalog and Parse share one name table so
// the WebUI palette matches POST /prompt. Discovery and caching belong to
// internal/resources; this package consumes one session's pinned snapshot.
package command
