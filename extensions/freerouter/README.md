# freerouter

ki provider extension that race-routes every request through OpenRouter's free
model tier (`:free` models). Select the `free-router / auto` model in ki and
each prompt is fanned out to the next available free models; whichever produces
content first wins, the rest are aborted, and failures go into a TTL cooldown
before rejoining the pool.

## Setup

1. This package lives at `{KI_HOME}/extensions/freerouter`; restart
   `ki serve` or `POST /v1/reload` after changes.
2. Provide an OpenRouter API key (free tier is enough) in one of:
   - `PUT /v1/providers/free-router/credential` with `{"apiKey": "sk-or-..."}`
   - the extension config `apiKey` field (`PATCH /v1/extensions/freerouter/config`)
   - `OPENROUTER_API_KEY` in the server environment
3. Select `FreeRouter / auto` for a session and send a prompt.

## Configuration

`GET/PATCH /v1/extensions/freerouter/config`. Defaults:

| key | default | meaning |
|---|---|---|
| `raceWidth` | 2 | available free models raced in parallel each round |
| `maxBatches` | 3 | candidate rounds per request (raceWidth × maxBatches unique models max) |
| `exhaustedTtlMs` | 90000 | cooldown after 429/5xx/400/422 |
| `slowTtlMs` | 15000 | cooldown after a first-token timeout |
| `firstTokenTimeoutMs` | 10000 | batch-1 first-token deadline (batch 2/3: 1.5x/2x) |
| `idleTimeoutMs` | 30000 | max silence from a winning stream |
| `refreshIntervalMs` | 3600000 | background refresh of the free model list |

## Security notes

- Prompts, conversation context, and tool definitions are sent to OpenRouter
  and whichever free provider wins the race. Do not route secrets, credentials,
  or regulated data through it.
- Free-model tool-calling support varies; models that reject tools are skipped
  automatically (400 → cooldown), which can shrink the usable pool.

## License

MIT
