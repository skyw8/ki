// Package web provides the production frontend embedded into ki and served by
// `ki serve` on same-origin non-/v1 paths.
//
// web/dist is build output and is not tracked by git. Build it with
// `cd web && bun run build`, then compile ki with `-tags embed`. Without that
// tag stub.go supplies an empty FS and the server reports the UI as not built.
//
// Development checks use incremental TypeScript and Bun unit tests. `bun run
// test` runs both unit tests and the isolated Playwright fake-model suite; the
// Go e2e harness and CI use this entry point to retain the complete test set.
// The fake browser runner reuses browser processes while keeping each test's
// context and each invocation's server state and artifacts isolated.
package web
