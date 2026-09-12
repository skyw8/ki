// Package web provides the production frontend embedded into ki and served by
// `ki serve` on same-origin non-/v1 paths.
//
// web/dist is build output and is not tracked by git. Build it with
// `cd web && bun run build`, then compile ki with `-tags embed`. Without that
// tag stub.go supplies an empty FS and the server reports the UI as not built.
package web
