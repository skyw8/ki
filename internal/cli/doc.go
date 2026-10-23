// Package cli is the Cobra command shell: load config, start or attach to the
// local HTTP server, and stream SSE to the terminal.
// The SSE reader restores connection-local message patches before printing
// newly accumulated text; message_end remains the authoritative full message.
// A 410 from an expired run replay recovers the latest human turn on the selected
// branch through compact metadata, index and exact body GETs, never another
// prompt. Other HTTP errors remain errors rather than empty successful replies.
//
//	ki                  detached server + WebUI, then best-effort browser open
//	ki serve [-d]       foreground or detached server + WebUI
//
// Bare `ki` is dual-purpose: a terminal user gets the server and a browser tab,
// and a Windows Explorer double-click gets the same without Cobra's
// command-line splash (MousetrapHelpText is cleared in Main). A double-click
// hides the console window and reports fatal errors in a message box, because
// that console closes with the process.
// The printed and opened WebUI URL carries server.json's token in a fragment.
// The SPA clears it before exchanging it for a browser session cookie, so a
// fresh browser needs no manual login. Treat this launcher link as a secret.
//
// A detached child reports a startup failure through the private
// KI_STARTUP_REPORT file and inherits /dev/null rather than a pipe, so the
// launcher can print the child's own reason (bad config, occupied port, …)
// instead of only "server did not come up"; io.Discard would make the child's
// log writes hit EPIPE once the launcher exits. Failures that never reach
// logging setup still surface this way. Daemon-only commands
// (reload/extension list) append a `ki serve -d` hint when no server is live.
//
//	ki run [flags] text create/resume session, POST prompt, print events
//	ki session compact  compact an existing session
//	ki session fork     fork an existing session
//	ki stop             stop the daemon through owned cleanup (signal fallback)
//	ki restart          stop, then start this binary on the recorded address
//	ki status           read-only addr/pid/version/exe of the live daemon
//	ki reload           POST /v1/reload on a live daemon (no in-process serve)
//	ki extension list   list globally discovered extensions
//	ki provider login   authenticate a provider extension (browser or device code)
//	ki provider logout  remove a provider extension credential
//	ki config path      print global/project ki.toml and mcp.json locations
//	ki version          print the build version
//
// Lifecycle is driven by server.json, which now carries pid/build/exe/
// startedAt next to addr/token (build, not version: `version` is reserved for
// the integer schema field, which server.json deliberately does not use). `ki status` compares that identity with the
// current binary; `ki stop` prefers POST /v1/shutdown, falls back to SIGTERM →
// SIGKILL for a build that predates the route, and removes the handshake only
// after checking its token still matches. `ki restart` and bare `ki` reuse the
// recorded address, so port-forwards and WebUI bookmarks survive an upgrade.
// A daemon running a different build is reported once as a warning by clients
// but never blocks them. Commands only ever act on this KI_HOME's handshake;
// they do not scan for unrelated ki processes on the machine.
//
// If server.json is healthy the client connects; otherwise it listens on
// 127.0.0.1:0 in-process and tears the server down on exit.
// --session is required to resume. --model is sent with the prompt and
// persisted on that session only. --thinking selects the model's advertised
// thinking level, including "<base> fast". New sessions receive it at creation;
// resumes PATCH thinking and an optional model atomically before posting the
// prompt, and a rejected PATCH never submits that prompt. Without --thinking,
// a resume keeps the existing settings flow without a preliminary PATCH.
// The session-show --thinking flag only displays transcript reasoning.
// Empty prompts and conflicting --steer / --queue are rejected before config
// loading or server/session effects. --steer / --queue override the busy-message
// default from toggles.json. KI_FAKE=1 injects provider.Scripted.
//
// Request flow: docs/architecture.md.
// The private __code-mode-worker entrypoint runs before Cobra/config/logging
// discovery and exposes only its parent's bounded stdio protocol, never HTTP.
package cli
