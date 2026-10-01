# freerouter

Go provider extension that race-routes requests through OpenRouter's free
model tier (`:free` models). The same process always exposes an OpenAI-compatible
Chat Completions HTTP proxy; when started as a ki sidecar it also speaks the
NDJSON JSON-RPC provider protocol.

## Build

```bash
go build -trimpath -o extensions/freerouter/bin/freerouter ./extensions/freerouter
# On Windows, use bin/freerouter.exe.
```

Build from the repository root with the root Go module. The executable contains
the entire runtime: it needs no source code, Go installation, or interpreter at
launch. For Ki, distribute `extension.json`, `locales/`, and the platform binary
in `bin/`. Standalone HTTP mode needs only the binary and its environment or CLI
configuration. Cross-compile with `GOOS` and `GOARCH`; no C compiler is required.

## Standalone HTTP proxy

```bash
cd extensions/freerouter
export OPENROUTER_API_KEY=sk-or-...
./bin/freerouter
# listens on 127.0.0.1:18427 by default
curl http://127.0.0.1:18427/healthz
curl http://127.0.0.1:18427/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

Point any OpenAI-compatible client at `http://127.0.0.1:18427/v1`.

Environment:

| variable | meaning |
|---|---|
| `OPENROUTER_API_KEY` / `FREEROUTER_API_KEY` | required for standalone |
| `OPENROUTER_BASE_URL` | default `https://openrouter.ai/api/v1` |
| `FREEROUTER_LISTEN` | default `127.0.0.1:18427`; overrides sidecar config |
| `FREEROUTER_RACE_WIDTH` | race width, clamped to 1–8; applies in both modes |
| `FREEROUTER_MAX_BATCHES` | standalone maximum rounds, clamped to 1–6 |

Use `serve` for explicit standalone mode. `--listen` and `--base-url` override
environment settings in standalone mode; `--verbose` / `-v` enables the verbose
CLI option. Sidecar mode reads `config.json` from `KI_EXTENSION_ROOT` (or its
working directory), then applies the documented environment fallbacks.

## ki extension (sidecar)

1. Place this package at `{KI_HOME}/extensions/freerouter` and ensure `bin/freerouter` exists.
2. Provide an OpenRouter key via `PUT /v1/providers/free-router/credential`, extension config `apiKey`, or `OPENROUTER_API_KEY`.
3. Select `freerouter / auto` for a session.
4. The sidecar also serves HTTP on the configured `listen` address (default `127.0.0.1:18427`) for other local programs.
   After at least one ki stream (or with config/`OPENROUTER_API_KEY` set), local clients can call the same port.

`extension.json` runtime:

```json
{ "runtime": { "kind": "rpc", "command": "bin/freerouter", "args": ["sidecar"] } }
```

## Configuration

`GET/PATCH /v1/extensions/freerouter/config` (extension mode) or env/CLI (standalone).

| key | default | meaning |
|---|---|---|
| `listen` | `127.0.0.1:18427` | HTTP bind address (reload required to change in sidecar) |
| `raceWidth` | 2 | free models raced in parallel each round |
| `maxBatches` | 3 | candidate rounds per request |
| `exhaustedTtlMs` | 90000 | cooldown after 429/5xx/400/422 |
| `slowTtlMs` | 15000 | cooldown after first-token timeout |
| `firstTokenTimeoutMs` | 10000 | batch-1 first-token deadline |
| `idleTimeoutMs` | 30000 | max silence from a winning stream |
| `refreshIntervalMs` | 3600000 | background refresh of the free model list |

The first visible text or tool call selects the winning model; empty and
reasoning-only responses do not win. Candidate rounds expand their first-token
timeout to 1.5× and then 2×. Losing streams and disconnected HTTP clients cancel
their upstream requests. An idle winning stream closes with an error instead of
starting another model after output has already been delivered.

Configuration updates reset cooldowns and keep the cached model list. Changing
`listen` requires restarting the sidecar. Configuration reads use Ki’s versioned
state helper and never overwrite unsupported newer configuration.

## Tests

```bash
go test -race ./extensions/freerouter
```

Tests preserve the original config, IR conversion, discovery, and cooldown
cases, and cover concurrent discovery, upstream errors, streaming tool calls,
UTF-8 text, usage, race failover, timeouts, cancellation, HTTP JSON/SSE/gzip, and
sidecar initialization, provider events, config reload, and shutdown.

## Security notes

- Prompts and tool definitions are sent to OpenRouter and whichever free provider
  wins the race. Do not route secrets through it.
- The HTTP proxy defaults to loopback only and has no TLS.
- Free-model tool-calling support varies; rejecting models are cooled down.

## License

MIT

## Source package fallback

To stage a source package that can build outside this checkout, run from the repository root:

```bash
go run ./scripts/build-extensions.go -source -out var/extensions-source -only freerouter
```

Copy `var/extensions-source/freerouter` to `{KI_HOME}/extensions/freerouter`. When `bin/freerouter` (`.exe` on Windows) is missing, Ki runs `go run ./install/main.go` at the package root. The package includes a standalone Go module and minimal shared Ki sources; it excludes private configuration, state, caches, and build output. Go is required for this first build. It builds the native executable with CGO disabled. Once the binary exists, installation is skipped and no compiler or source files are needed to launch it. Default binary packages retain the same manifest metadata but omit sources and the installer; replace an incomplete binary package with a complete binary or source package.
