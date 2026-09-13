# deep-web-search

`deep-web-search` is a global Ki sidecar that registers `deep_web_search`,
`fetch_content`, `get_search_content`, and `source_check`.

It deliberately combines different source types instead of registering four
model providers:

- `codex` reads the existing `KI_HOME/credentials.json` OAuth value written by
  `codex-oauth` and calls the Codex Responses web-search tool.
- `exa` uses `/search` or `/answer` with `exaApiKey`, and uses Exa MCP when no
  key is configured.
- `tinyfish` uses `tinyfishApiKey` (or `TINYFISH_API_KEY`) for search and fetch.
- `duckduckgo` uses the public HTML endpoint and requires no credential or
  self-hosted service.

The sidecar keeps provider credentials out of tool results, jsonl entries,
progress events, and logs. Search results are URL-normalized, deduplicated,
ranked with reciprocal-rank style scoring, capped for domain diversity, and
stored in a short-lived private cache. `includeContent` fetches readable public
HTTP(S) pages with redirect, SSRF, size, and timeout checks; TinyFish Fetch is
used as a fallback when configured. For `deep_web_search` the bodies are
hydrated off the critical path: the source pack returns as soon as providers
settle, and `get_search_content` reads the bodies once the background pass
finishes. `source_check`, which needs passages in-band, hydrates inside the
same bounded budget.

The user-configured `workflow` setting is `none` or `auto-summary`. It is kept
in the extension config and is intentionally not exposed as a
`deep_web_search` tool argument, so the model cannot override the user's
choice for an individual call:

- `none` returns a compact source pack and never opens a browser or calls a
  model.
- `auto-summary` calls the configured `summaryModel` without opening a
  browser. Missing, timed-out, or failed model completion falls back to
  `none` and returns the source pack.

`codexModel` and `summaryModel` each accept an optional thinking effort
(`codexThinkingEffort` / `summaryThinkingEffort`). The WebUI renders the
thinking-effort dropdown next to each model picker; an empty value uses the
model default, and `off` omits the Responses reasoning block.

When `queries` contains multiple search strings, all queries are started in
parallel. Each query also starts all enabled providers in parallel; a provider
failure is isolated and successful query/provider results are still aggregated.
Tool result details include `searchDurationMs`, plus each successful provider's
`durationMs` in `providerRuns` and each failed provider's `durationMs` in
`diagnostics`.

Every stage runs under a budget from `src/deadlines.ts`. Each plain-HTTP
provider gets `providerSearch`; once a quorum of them is in, their stragglers
are cut after `providerGrace`, so one slow index cannot hold the call. Codex
gets the longer `codexSearch` budget and is exempt from that cut: a query that
includes Codex waits for it up to `codexSearch`, because it runs a reasoning
model with the hosted `web_search` tool rather than a plain index lookup.
Content hydration has its own `providerContent` budget, and the whole tool
resolves inside `tool`, well under the host hard timeout declared by `timeoutMs`.
A cut provider appears in `diagnostics` with `category: "timeout"`, and the host
keeps the partial result a cancelled sidecar still returns.

The source implementation uses only Node standard-library modules. Vite bundles
`src/main.ts` into a single ESM file (`bun run build`), and the sidecar runs the
bundle with bun (`bun dist/main.js`). `extension.json` `runtime.install` runs
`bun run setup` (`bun install` + `bun run build`), so `dist/` is produced on
install and is not checked in. Run `bun test` from this directory for protocol
and toggle tests.
