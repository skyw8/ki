# freerouter

Rust provider extension that race-routes requests through OpenRouter's free
model tier (`:free` models). The same process always exposes an OpenAI-compatible
Chat Completions HTTP proxy; when started as a ki sidecar it also speaks the
NDJSON JSON-RPC provider protocol.

## Build

```bash
cd extensions/freerouter
cargo build --release
cp target/release/freerouter bin/freerouter
```

## Standalone HTTP proxy

```bash
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
| `FREEROUTER_LISTEN` | default `127.0.0.1:18427` |

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

## Security notes

- Prompts and tool definitions are sent to OpenRouter and whichever free provider
  wins the race. Do not route secrets through it.
- The HTTP proxy defaults to loopback only and has no TLS.
- Free-model tool-calling support varies; rejecting models are cooled down.

## License

MIT
