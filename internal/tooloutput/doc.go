// Package tooloutput stores complete text tool results outside the model
// context and returns bounded previews with a readable file reference.
//
// Stores are session-scoped. The loop applies them after tool and extension
// hooks so every model-facing tool result follows the same size, quota, and
// cleanup policy, and shell task logs live in the same session directory
// instead of a second temporary-file scheme.
//
// A store owns one run root under the base directory; the base directory is
// swept at startup so a crashed server cannot leak output forever (see
// owner.go). Files are written with filepath, 0700 directories, and 0600
// files, and they are removed when the session or the server closes.
// Parameter and result tables: docs/tools.md.
package tooloutput
