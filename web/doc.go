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
// Automated browser checks use Chromium only, including desktop/tablet/phone
// viewport and touch profiles. The fake runner reuses browser processes while
// keeping each test's context and each invocation's server state and artifacts
// isolated. Independent specs opt into per-test process scheduling only after
// every case has passed standalone; ordered scenarios stay together.
// Escape dismisses a drawer's open child menu before the drawer, including
// the interval before the menu's initial focus frame has run.
// New sessions send only model selections validated against the current
// catalog. Missing or not-yet-loaded selections defer to the server default;
// browser preferences never force an unavailable extension provider.
// Model defaults come from the server; workspace expansion is page-local.
// Auth and push ready identify a server instance, not merely a browser origin.
// Launcher URLs carry #token=... for automatic cookie login. Startup strips
// that fragment before requests and consumes it once, never persisting it or
// replaying it after a backend change; plain URLs retain manual token login.
// Backend changes remount runtime state and recheck authentication; CSRF cookie
// names and expiring cross-tab focus markers are scoped to that instance.
// Web Push subscriptions are reused only when their VAPID key still matches.
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
// Extension inspectors show complete operation/runtime errors above either tab;
// extension errors and transcript cancellations share the plain red notice style.
// Info owns live runtime progress: Agents precedes Processes, task paths form
// collapsible agent trees, and processes group by owner. Names navigate to the
// owner's conversation; controls address the real session/process owner.
// Agent rows, including leaves, share one disclosure column so nested names
// advance uniformly to the right; plain rows avoid competing card insets.
// Runtime notifications use one rounded dashed card with a subtle themed fill,
// never a second inner bubble frame or human input actions. Duration
// digits use compact left-aligned format slots and tabular numbers at full size.
// Context replaces the WebUI trace surface, not CLI trace diagnostics. It
// reconstructs the active branch before a selected model request from persisted
// headers and messages, with explicit missing-body and approximation notices.
// System sources are structural hints, not authoritative extension provenance;
// unwrapped operator append text cannot identify global versus project origin.
// Model usage is authoritative; category token estimates and non-text payloads
// never claim exact encoded-input counts. Bodies load only on explicit expansion.
// Metadata-only history retains estimates derived from full stored bodies.
// Colored category bars and reported-input lines use independent axes; neither
// missing bodies nor large remote usage can flatten known category segments.
// One categorical palette is shared by charts, legends, browser and events;
// human inputs use warm red while tool results use cool teal.
// Remote checkpoints encapsulate prior categories, not local plaintext summaries.
// Context event navigation resolves an actual same-branch chat row and starts
// its remounted viewport in reading intent while history is still loading.
// See docs/webui.md for lifecycle, responsive geometry and touch contracts.
package web
