## description
A lean and extensible agent runtime designed for easy integration.

## tech stack
- Go: backend
- CLI: Cobra + Viper
- WebUI: Vite 8 (Rolldown) + React (deps and scripts use bun; vite config unchanged)

## directory structure

```
ki/
├── cmd/ki/              sole binary
├── web/                 Vite+React SPA; go:embed dist; same-origin from serve
│   ├── src/             app entry; api/ (client, types); lib/ (pure logic); hooks/; i18n/; components/ (shared UI)
│   │   └── features/    domain UI + logic: chat/ attachments/ markdown/ sessions/ settings/
│   ├── scripts/         e2e-parallel.ts (Playwright runner)
│   └── e2e/             Playwright specs
├── e2e/                 CLI e2e: scripted model for tests; -tags live for a real model
├── docs/                cross-package notes (see Docs below)
├── spec/                TLA+ formal specs (events-wait: SSE close-before-broadcast ordering)
├── extensions/          bundled extension packages (install under {KI_HOME}/extensions)
├── internal/
│   ├── cli/             flags; start/attach server; SSE to the terminal
│   ├── server/          HTTP orchestration + embedded WebUI
│   ├── loop/            main loop; emit only
│   ├── session/         jsonl tree
│   ├── tools/           model-aware builtins (see internal/tools/doc.go)
│   ├── search/          embedded ripgrep engines for Grep / Glob
│   ├── provider/        catalog, registry, credentials, cost, Ki adapter
│   ├── prompt/          system prompt renderer
│   ├── resources/       session-scoped runtime and filesystem snapshots
│   ├── compact/         compaction
│   ├── config/          merge ki.toml and env
│   ├── command/         slash catalog and parse
│   ├── skills/          discover SKILL.md
│   ├── toggles/         {KI_HOME}/toggles.json
│   ├── extension/       extension.json + NDJSON sidecar
│   ├── workspace/       workspace registry
│   ├── types/           Message / Usage IR
│   ├── idgen/           session / entry id
│   └── logging/         JSONL stderr + rotated ki.jsonl
├── AGENTS.md
├── README.md
├── pkg/
│   └── llmprotocol/     reusable Completions / Responses / Anthropic clients
└── go.mod
```

## docs

- Cross-package notes live in `docs/`, one file per topic:
  - `architecture.md` — prompt flow across cli → server → loop
  - `events.md` — unified loop/SSE, extension, provider, and WebUI event catalog
  - `system_prompt.md` — prompt layers, resource cache, dynamic inputs, reload
  - `session.md` — session dir layout, append-only jsonl tree
  - `provider.md` — provider registry and protocol shapes (Completions / Responses / Anthropic)
  - `extension.md` — extension.json packages, toggles, sidecar JSON-RPC, lifecycle
  - `tools.md` — tool contract (names/schemas follow Claude Code, results follow pi)
  - `webui.md` — same-origin WebUI serving contract
  - `workspace.md` — workspace registry (`{KI_HOME}/workspaces.json`)
  - `postmortem/` — retrospective entries
- Package invariants are in each package's `doc.go` (`go doc ./internal/session`). Keep this file free of `todo` paths and of per-file inventories under those directories.

## guide

- Dev run: `scripts/run.sh` rebuilds `web/dist` (skipped when frontend inputs are unchanged; `--force-web` forces it) and `./ki`, then starts `ki serve` with the real configured provider by default inside a tmux session named `ki`: window `server` runs the daemon, window `cli` is a shell for operating it. Re-run to rebuild and respawn; real tests operate through the script or `tmux attach -t ki`. The script compiles with `-tags embed`; `--fake` is an explicit opt-in for canned-model plumbing checks only.
- Fake model (tests only): `go test ./e2e` (`KI_FAKE=1`; CLI main path, `serve`, `serve -d`, two sessions in parallel, WebUI Playwright). Do not use `KI_FAKE=1` or `--fake` for normal development, manual verification, or service restarts.
WebUI: `cd web && bun run test:e2e` (parallel runner; each unit starts an isolated fake `ki serve`; `bun run test:e2e:serial` runs one process for debugging). Requires `bunx playwright install chromium`. Long-history / huge-message budgets: `cd web && bun run test:perf` (not in the fake matrix).
- Live model: `go test -tags live -timeout 5m ./e2e -run Live` covers DeepSeek `deepseek-flash` across Completions, Responses, and Anthropic (reads `DEEPSEEK_API_KEY` or `~/.ki` credentials; images / PDF / WebUI Playwright). `go test -tags live ./internal/provider -run TestLiveDeepSeek` drives the three protocol adapters directly.
WebUI live: `cd web && bun run test:e2e:live`.

## constraints

- Keep this list concise.
- Write this file in English.
- Write source-code comments in English.
- Do not consider backward compatibility. Prioritize refactoring legacy code and improving the existing implementation.
- Create PlantUML diagrams when necessary. When creating diagrams, only include PlantUML code blocks in Markdown files.
- Port-forwarded WebUI is a first-class client, including Tailscale and other high-latency links: same-origin relative `/v1` and `/assets` only; never navigate the browser to a host filesystem path; never use a native OS file picker; keep every text response gzip-compressed (`text/event-stream` excepted) and content-hashed `assets/` immutable, so a reload neither re-downloads nor revalidates large payloads over the link.
- WebUI must support mobile and touch layouts: use responsive drawers/full-screen dialogs, dynamic viewport and safe-area insets, at least 40px touch targets (44px for key navigation), and keep the responsive Playwright matrix in `web/e2e/responsive.spec.ts` passing; document detailed behavior in `docs/webui.md`.
- Cross-platform (Linux, macOS, Windows): `filepath` for joins; host-absolute paths; no POSIX-only roots, separators, or hidden-file rules.
- Do not name  `docs/todo`, or files under them in this file.
- Package comments go in that package's `doc.go`. Cross-package explanation stays in `docs/`.
- When changing code, update the related docs that already describe that contract (`docs/*.md` and the owning `doc.go`). Do not add new todo filenames here.
- Bugs and pitfalls get a why-comment at the fix site explaining why the code is written that way. A problem that recurs gets a retrospective entry under `docs/postmortem/`.
- One binary: `ki serve` serves API and the embedded SPA on the same origin. `web/dist` is untracked build output: build it (`cd web && bun run build`) and compile with `-tags embed`. Without the tag, `web/stub.go` supplies an empty FS and `ki serve` reports the UI as not built; serve does not run vite or bun.
- Naming: `extension` is the installable/runtime bundle; a provider supplied by one is an `extension provider`. Do not use `plugin` for provider code, APIs, runtime values, or UI/docs.
- Real provider is the default runtime. `KI_FAKE=1` and `scripts/run.sh --fake` are test-only opt-ins and must not be used for normal development or manual verification.
- Keep tests fast without weakening coverage: parallelize or cache independent work (WebUI runner `web/scripts/e2e-parallel.ts`; compile each sidecar fixture once per test process), but never drop, skip, or reorder tests for speed; a coverage regression must fail the run (`test:e2e:serial` is the single-process debug fallback).
- Do not invent REST routes for data the loop already has. Extend `loop.Event` and jsonl (and existing SSE / `GET /v1/sessions/{id}`) instead.
- A second prompt on a busy session steers the current run or queues for the next (`delivery` / `toggles.json` `message.busy`). `parentId` while busy is **409**. Resume requires `--session`. `--model` is per-session `config.json`, not toml.
