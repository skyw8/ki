# Ki

An extensible agent runtime designed for easy integration with other applications.

## Build

```bash
go build -o ki ./cmd/ki
```

## Run

```bash
# dev loop: build + run in a tmux session (window `server`, shell `cli`)
scripts/run.sh

# open the WebUI (starts a detached server and tries to open a browser)
./ki

# foreground server (writes ~/.ki/server.json)
./ki serve --addr 127.0.0.1:19800

# detached server
./ki serve -d

# one-shot prompt (starts an in-process server if none is up)
./ki run "what is in this repo?"
./ki run --session <id> "continue"
./ki run --session <id> --model openai/gpt-4o "switch model"
./ki session compact --session <id>
./ki session fork --session <id>

# inspect config and version
./ki config path
./ki version
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
