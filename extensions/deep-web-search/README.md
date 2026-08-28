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
used as a fallback when configured.

The `workflow` option is `none`, `summary-review`, or `auto-summary`:

- `none` returns a compact source pack and never opens a browser or calls a
  model.
- `summary-review` starts a local tokenized curator page. It supports source
  selection, additional search, model-assisted query rewrite, and summary
  draft generation. A curator timeout/cannot-start fallback returns `none`.
- `auto-summary` calls the configured `summaryModel` without opening a
  browser. Missing, timed-out, or failed model completion falls back to
  `none` and returns the source pack.

The source implementation uses only Node standard-library modules. The
checked-in `dist/main.js` is a small loader so the extension runs without an
install step. Run `npm test` from this directory for protocol and toggle
tests.
