## description
A lean and extensible agent runtime designed for easy integration.

## tech stack
- go

## directory structure

```
ki/
├── cmd/ki/              sole binary
├── web/                 Vite+React SPA; go:embed dist; same-origin from serve
├── e2e/                 CLI e2e: fake model by default; -tags live for a real model
├── docs/                cross-package notes (architecture, session, provider, tools, webui)
├── internal/
│   ├── cli/             flags; start/attach server; SSE to the terminal
│   ├── server/          HTTP orchestration + embedded WebUI
│   ├── loop/            main loop; emit only
│   ├── session/         jsonl tree
│   ├── tools/           Read / Write / Edit / Bash
│   ├── provider/        Completions / Responses / Anthropic
│   ├── prompt/          layered system prompt
│   ├── compact/         compaction
│   ├── config/          merge ki.toml and env
│   ├── skills/          discover SKILL.md
│   ├── mcp/             .mcp.json → loop.Tool
│   ├── workspace/       workspace registry
│   ├── types/           Message / Usage IR
│   ├── idgen/           session / entry id
│   └── klog/            stderr + ki.log
├── AGENTS.md
├── README.md
└── go.mod
```

## docs

Cross-package notes are in `docs/`. Package invariants are in each `doc.go` (`go doc ./internal/session`). Keep this file free of `todo` paths and of per-file inventories under those directories.

Dev run: `scripts/run.sh` builds `./ki` and starts `ki serve` inside a tmux session named `ki`: window `server` runs the daemon, window `cli` is a shell for operating it. Re-run to rebuild and respawn; real tests operate through the script or `tmux attach -t ki`. `--web` rebuilds `web/dist`; `--fake` uses the canned model.

Fake model: `go test ./e2e` (`KI_FAKE=1`; CLI main path, `serve`, `-d`, two sessions in parallel, WebUI Playwright).
WebUI: `cd web && npm run test:e2e` (starts a fake `ki serve`). Requires `npx playwright install chromium`.
Live model: `go test -tags live -timeout 5m ./e2e -run Live` (reads `DASHSCOPE_CN_API_KEY` or `~/.ki/ki.toml` dashscope-cn; default `qwen3.7-plus`; images / PDF / WebUI Playwright).
WebUI live: `cd web && npm run test:e2e:live`.

## constraint

- Keep this list concise.
- Port-forwarded WebUI is a first-class client: same-origin relative `/v1` and `/assets` only; never navigate the browser to a host filesystem path; never use a native OS file picker.
- Cross-platform (Linux, macOS, Windows): `filepath` for joins; host-absolute paths; no POSIX-only roots, separators, or hidden-file rules.
- Write this file in English.
- Do not name  `docs/todo`, or files under them in this file.
- Package comments go in that package's `doc.go`. Cross-package explanation stays in `docs/`.
- When changing code, update the related docs that already describe that contract (`docs/*.md` and the owning `doc.go`). Do not add new todo filenames here.
- Bugs and pitfalls get a why-comment at the fix site explaining why the code is written that way. A problem that recurs gets a retrospective entry under `docs/postmortem/`.
- One binary: `ki serve` serves API and the embedded SPA on the same origin. Rebuild `web/dist` then `go build` after frontend changes; serve does not run npm.
- Do not invent REST routes for data the loop already has. Extend `loop.Event` and jsonl (and existing SSE / `GET /v1/sessions/{id}`) instead.
- A second prompt on a busy session is **409**. Resume requires `--session`. `--model` is per-session `config.json`, not toml.