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
//
// TranscriptStore commits replica facts outside React scheduling. SessionSync
// owns reader/recovery/ACK authority; TranscriptRequests owns cancellable
// history/body projections. Human-turn identity and body coverage are shared
// across chat, navigation and statistics. Lifecycle status uses durable entry
// identities, never presentation order or text equality.
//
// Resume reconciles independently of push heartbeats; idle snapshots retire
// transient work. One viewport owner controls reading/following/seeking intent,
// keyed anchors and virtualizer compensation. Changes to running-placeholder
// padding reconcile through that owner only while already following.
// Local Markdown formatting and row expansion join pre-paint measurements even
// when the parent transcript node is unchanged.
// WebKit native motion retains resize corrections as logical/visual offsets
// until a single sign-ordered transfer can preserve the physical viewport.
// See docs/webui.md for lifecycle, responsive geometry and touch contracts.
package web
