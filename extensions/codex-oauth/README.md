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

## Pricing estimates

Model costs use the same OpenAI Standard USD prices per million tokens as the
built-in `openai` catalog, verified on 2026-10-08. This includes input, output,
cache reads, cache writes, and the long-context tier above 272,000 input tokens
(cached tokens included). See the [price table](../../docs/provider.md#gpt-价格).

These are API-equivalent usage estimates, **not actual OAuth subscription
charges**. Included usage and purchased credits follow the
[Codex rate card](https://help.openai.com/en/articles/20001106-codex-rate-card).
Ki does not convert subscription limits or credits to dollars, or apply
Batch/Flex/Fast or regional pricing adjustments.

## Remote compaction

Every bundled model advertises
`compaction: {"standalone": "codex-v2"}`. The sidecar
implements that Codex-specific protocol by sending a normal streaming
`POST /codex/responses` request with a final
`{"type":"compaction_trigger"}` input item. This is intentionally distinct
from OpenAI's public `POST /responses/compact` and `context_management`
interfaces. The next canonical window keeps recent client-authored user turns
within Codex's retained-message budget, followed by the provider's complete
opaque output. Cancellation closes only that request's active response and
reports `Codex compaction was cancelled` whether it happens before response
headers or during body reads, preserving the context cause. The
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

The installation id is shared by Codex OAuth and Codex search sidecars through
versioned `{KI_HOME}/codex-client.json`, with a cross-process creation lock.
Window ids and turn routing tokens are bounded process-local state. A successful
remote checkpoint advances the window and invalidates its old WebSocket baseline.
The host supplies a logical turn UUID that stays stable across retries and tool
continuations. Compaction metadata does not invent a manual trigger/reason when
the host has not supplied one.

Sending the trigger without these headers yields
`Codex Remote Compaction V2 expected exactly one compaction output item, got 0
from 0 output items`.

## Context windows

`contextWindow` follows the OpenAI Codex bundled catalog
(`codex-rs/models-manager/models.json`), which caps `gpt-6.1-sol`,
`gpt-6-astra`, `gpt-6-luna`, and the GPT-5.6 Sol/Terra/Luna variants at 272,000 tokens for coding clients
(`max_context_window` 872,000 is a configuration override, not the default).
The model API spec pages advertise 1,050,000, but the ChatGPT-backed Codex
service this extension talks to enforces the smaller window. The built-in
`openai` catalog entries use the same value so a Codex-issued credential and a
direct API key see one consistent context window.

## Thinking levels

Every supported base thinking level has an adjacent Fast variant, for example
`low` / `low fast`, `medium` / `medium fast`, and `max` / `max fast`.
Models that support `off` also expose `off fast`; unsupported base levels remain
hidden. Defaults stay ordinary (prefer `medium`). There is no separate `/fast`
toggle: the session stores the selected `thinkingEffort`, and the sidecar splits
it before encoding the request. `high fast` sends `reasoning.effort: "high"` and
`service_tier: "priority"`; `off fast` omits reasoning while retaining priority.
Ordinary choices omit the service tier. The routing-hint header agrees with the
body. Switching to a model without Fast drops only the speed suffix before
clamping the base effort.

The WebUI exposes these options wherever it displays model thinking levels.
The CLI uses `ki run --thinking "high fast" "<prompt>"`; resume with
`--session <id>` applies model/thinking through the existing session PATCH and
rejects a busy session rather than silently changing its ongoing request.

`thinkingLevelMap` mirrors the built-in catalog entries for the same models.
No GPT-6.1/6/5.6 variant accepts `minimal`, so every map hides it. The GPT-5.6
models and GPT-6 Luna accept `none` (the API lists `none, low, medium, high,
xhigh, max`), so `off` stays visible and maps to the zero-reasoning floor; the
sidecar skips the whole `reasoning` block for `off` anyway. GPT-6.1 Sol and GPT-6
Astra reject `none` (their `reasoning.effort` supports only `low`, `medium`,
`high`, `xhigh`, and `max`), so their maps hide `off` as well.

## Verification

Run `go test ./extensions/codex-oauth` from the repository root. The tests
retain reference request bodies and complete stream event snapshots captured
from the original implementation's 33 tests, plus native HTTP checks for
metadata, refresh result shapes, header and idle timeouts, isolated
cancellation, browser login, manual callback input, and device authorization.
Provider-owned numeric values and unknown compaction fields remain opaque.
Request metadata is added to a shallow copy of the body; nested history and
tool schemas are read-only and are not recursively copied a second time.
Measure history-heavy request construction with
`go test -run '^$' -bench BenchmarkCodexRequestHistory -benchmem ./extensions/codex-oauth`.

## Responses transport

Generation prefers Codex Responses WebSockets, with isolated connection reuse
and incremental input only when the canonical prefix and request properties
match. Upgrade rejection can use HTTP fallback; a stream that already delivered
events is not blindly resubmitted. Model/tier/credential/endpoint changes cannot
reuse another route's sticky token. Cancellation invalidates that connection
without affecting another session. HTTP fallback and standalone remote
compaction send zstd-compressed request bodies.

The WebSocket cache admits at most 64 leases (including gate waiters), evicts
inactive connections after five minutes, and falls back to HTTP on local capacity
pressure. Incremental baselines retain serialized prefixes rather than complete
history object graphs, capped at 4 MiB per connection and 32 MiB globally.
Oversized histories still work through full requests. Routing metadata retains
at most 256 bindings and eight logical-turn tokens per binding.

Client headers and metadata follow the referenced Codex CLI protocol, including
`originator: codex_cli_rs`, the CLI User-Agent shape, beta flags, logical identity,
and `x-codex-routing-hint`. This is protocol alignment, not a promise that TLS,
prompts, tools, or optional attestation are indistinguishable from the official
binary. The service decides account eligibility, actual speed, and usage limits;
the catalog's speed description is not a subscription billing multiplier.

## Source package fallback

To stage a source package that can build outside this checkout, run from the repository root:

```bash
go run ./scripts/build-extensions.go -source -out var/extensions-source -only codex-oauth
```

Copy `var/extensions-source/codex-oauth` to `{KI_HOME}/extensions/codex-oauth`. When `bin/codex-oauth` (`.exe` on Windows) is missing, Ki runs `go run ./install/main.go` at the package root. The package includes a standalone Go module and minimal shared Ki sources; it excludes private configuration, state, caches, and build output. Go is required for this first build. It builds the native executable with CGO disabled. Once the binary exists, installation is skipped and no compiler or source files are needed to launch it. Default binary packages retain the same manifest metadata but omit sources and the installer; replace an incomplete binary package with a complete binary or source package.
