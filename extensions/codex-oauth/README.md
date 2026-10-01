# Codex OAuth provider extension

This directory contains the Go source for a standalone Ki provider extension.
Build it from the repository root:

```bash
go build -o extensions/codex-oauth/bin/codex-oauth ./extensions/codex-oauth
```

Install the `extension.json`, `locales/`, and `bin/` files under
`{KI_HOME}/extensions/codex-oauth`. Ki launches `bin/codex-oauth` directly;
Windows builds use `bin/codex-oauth.exe`. The executable embeds its provider
catalog and needs no Go installation, source files, or working-directory data
to launch. It speaks NDJSON RPC on stdin/stdout. Rebuild the executable and
restart or reload Ki after changing the source.

The provider appears as `openai-codex` in
the provider settings. Use Browser login locally, or Device code login when
the WebUI is reached through a port forward. `KI_CODEX_AUTH_BASE_URL` and
`KI_CODEX_CALLBACK_PORT` are test-only endpoint overrides; normal use talks to
the OpenAI Codex service endpoints defined in `extension.json`.

## Remote compaction

Every bundled model advertises
`compaction: {"standalone": "codex-v2"}`. The sidecar
implements that Codex-specific protocol by sending a normal streaming
`POST /codex/responses` request with a final
`{"type":"compaction_trigger"}` input item. This is intentionally distinct
from OpenAI's public `POST /responses/compact` and `context_management`
interfaces. The next canonical window keeps recent client-authored user turns
within Codex's retained-message budget, followed by the provider's complete
opaque output. Cancellation closes only that request's active response. The
window is replayed only within the matching provider/model/credential/protocol
binding.

The ChatGPT Codex backend only honors the appended `compaction_trigger` for a
request that identifies itself as the Codex CLI, so the sidecar mimics the
client headers the Codex CLI sends on every Responses request:

- `originator: codex_cli_rs` and a matching
  `User-Agent: codex_cli_rs/<version> (...)`;
- `x-codex-beta-features: remote_compaction_v2` (the CLI advertises this
  unconditionally; without it the backend ignores the trigger and completes
  with an empty output, so no `compaction` item is returned);
- `session-id`, `thread-id`, and `x-client-request-id`;
- `x-codex-window-id` plus a body `client_metadata` map carrying the same
  `x-codex-window-id`, a per-process `x-codex-installation-id`, and
  `session_id` / `thread_id` / `turn_id`;
- `x-codex-turn-metadata` (mirrored into `client_metadata`) with
  `request_kind: "turn"` for ordinary turns and `request_kind: "compaction"`
  plus the Remote Compaction V2 descriptor for the compaction request.

The window id is a client-generated UUID kept per session for the sidecar's
lifetime; the installation id is minted once per sidecar process (Codex
persists it, Ki does not).

Sending the trigger without these headers yields
`Codex Remote Compaction V2 expected exactly one compaction output item, got 0
from 0 output items`.

## Context windows

`contextWindow` follows the OpenAI Codex bundled catalog
(`codex-rs/models-manager/models.json`), which caps `gpt-6.1-sol`,
`gpt-6-astra`, and the GPT-5.6 Sol/Terra/Luna variants at 272,000 tokens for coding clients
(`max_context_window` 872,000 is a configuration override, not the default).
The model API spec pages advertise 1,050,000, but the ChatGPT-backed Codex
service this extension talks to enforces the smaller window. The built-in
`openai` catalog entries use the same value so a Codex-issued credential and a
direct API key see one consistent context window.

## Thinking levels

`thinkingLevelMap` mirrors the built-in catalog entries for the same models.
No GPT-6.1/6/5.6 variant accepts `minimal`, so every map hides it. The GPT-5.6
models accept `none` (the API lists `none, low, medium, high, xhigh, max`), so
`off` stays visible and maps to the zero-reasoning floor; the sidecar skips the
whole `reasoning` block for `off` anyway. GPT-6.1 Sol and GPT-6 Astra reject
`none` (their `reasoning.effort` supports only `low`, `medium`, `high`,
`xhigh`, and `max`), so their maps hide `off` as well.

## Verification

Run `go test ./extensions/codex-oauth` from the repository root. The tests
retain reference request bodies and complete stream event snapshots captured
from the original implementation's 33 tests, plus native HTTP checks for
metadata, refresh result shapes, header and idle timeouts, isolated
cancellation, browser login, manual callback input, and device authorization.
Provider-owned numeric values and unknown compaction fields remain opaque.

## Source package fallback

To stage a source package that can build outside this checkout, run from the repository root:

```bash
go run ./scripts/build-extensions.go -source -out var/extensions-source -only codex-oauth
```

Copy `var/extensions-source/codex-oauth` to `{KI_HOME}/extensions/codex-oauth`. When `bin/codex-oauth` (`.exe` on Windows) is missing, Ki runs `go run ./install/main.go` at the package root. The package includes a standalone Go module and minimal shared Ki sources; it excludes private configuration, state, caches, and build output. Go is required for this first build. It builds the native executable with CGO disabled. Once the binary exists, installation is skipped and no compiler or source files are needed to launch it. Default binary packages retain the same manifest metadata but omit sources and the installer; replace an incomplete binary package with a complete binary or source package.
