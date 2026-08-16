# Ki

An extensible agent runtime designed for easy integration with other applications.

## Build

```bash
go build -o ki ./cmd/ki
```

## Run

```bash
# foreground server (writes ~/.ki/server.json); open http://127.0.0.1:19800/
./ki serve --addr 127.0.0.1:19800

# detach
./ki -d

# one-shot prompt (starts an in-process server if none is up)
./ki "what is in this repo?"
./ki --session <id> "continue"
./ki --session <id> --model openai/gpt-4o "switch model"
./ki --session <id> compact
./ki --session <id> fork
```

Auth is a Bearer token in `~/.ki/server.json` (or `KI_HOME/server.json`). Config is `~/.ki/ki.toml` and `<cwd>/.ki/ki.toml`. Set `KI_FAKE=1` to use a canned model for local plumbing tests.

## Test

```bash
go test ./...
go test ./e2e
cd web && npm run test:e2e
cd web && npm run test:e2e:live
go test -tags live -timeout 5m ./e2e -run Live
```

Live tests call DashScope `qwen3.7-plus` (`dashscope-cn`). Put the key in `~/.ki/ki.toml` or `DASHSCOPE_CN_API_KEY`.

See `docs/prd/plan.md` for the first-version scope.