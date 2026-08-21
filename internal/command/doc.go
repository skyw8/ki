// Package command parses slash input and lists/expands commands.
//
// Builtins (compact, reload) are dispatched by the server. Skill and prompt
// templates expand to user text. Catalog and Parse share one name table so
// the WebUI palette matches POST /prompt. Template files are cached per
// session (home, cwd, session id); InvalidateAll drops that table.
package command
