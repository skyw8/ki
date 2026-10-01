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

Every stage runs under a budget from `http.go`. Each plain-HTTP
provider gets `providerSearch`; once a quorum of them is in, their stragglers
are cut after `providerGrace`, so one slow index cannot hold the call. Codex
gets the longer `codexSearch` budget and is exempt from that cut: a query that
includes Codex waits for it up to `codexSearch`, because it runs a reasoning
model with the hosted `web_search` tool rather than a plain index lookup.
Content hydration has its own `providerContent` budget, and the whole tool
resolves inside `tool`, well under the host hard timeout declared by `timeoutMs`.
A cut provider appears in `diagnostics` with `category: "timeout"`, and the host
keeps the partial result a cancelled sidecar still returns.

The extension is written in Go and uses the Go standard library for HTTP and
content parsing. Build and stage all bundled extensions from the repository root:

```bash
go run ./scripts/build-extensions.go -only deep-web-search
```

Copy `var/extensions/deep-web-search` to `{KI_HOME}/extensions/deep-web-search`.
The staged package contains its manifest, locales, and `bin/deep-web-search`
(`.exe` on Windows). Launching it needs no source code, Go compiler, Bun, or Node.
The RPC tool schemas are embedded in the executable; the protocol remains
language-independent.

```bash
go test -race ./extensions/deep-web-search
```

Configuration, cache, and credential documents use versioned atomic state
operations. A document newer than the extension understands is never overwritten.

Source URLs and content redirects use a pure Go WHATWG URL parser, preserving the original JavaScript URL normalization for international hostnames, encoded dot segments, backslashes, and numeric IPv4 forms. Content safety checks run against the normalized host before every request and redirect.

## Source package fallback

To stage a source package that can build outside this checkout, run from the repository root:

```bash
go run ./scripts/build-extensions.go -source -out var/extensions-source -only deep-web-search
```

Copy `var/extensions-source/deep-web-search` to `{KI_HOME}/extensions/deep-web-search`. When `bin/deep-web-search` (`.exe` on Windows) is missing, Ki runs `go run ./install/main.go` at the package root. The package includes a standalone Go module and minimal shared Ki sources; it excludes private configuration, state, caches, and build output. Go is required for this first build. It builds the native executable with CGO disabled. Once the binary exists, installation is skipped and no compiler or source files are needed to launch it. Default binary packages retain the same manifest metadata but omit sources and the installer; replace an incomplete binary package with a complete binary or source package.
