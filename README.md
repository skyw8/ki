# Ki

An extensible agent runtime designed for easy integration with other applications.

## Build

`web/dist` is build output and is not tracked by git, so build the SPA first and
compile with the `embed` tag to get the single binary with the WebUI:

```bash
cd web && bun install && bun run build
cd .. && go build -tags embed -o ki ./cmd/ki
```

Without `-tags embed`, `go build ./cmd/ki` produces the CLI/API only; `ki serve`
then reports the UI as not built. `scripts/run.sh` builds `web/dist` on demand and
always uses `-tags embed`.

On Windows, Ki looks for Git Bash through `KI_GIT_BASH_PATH`, `CLAUDE_CODE_GIT_BASH_PATH`, standard Git for Windows locations, and then `bash.exe` on `PATH`. If Bash is unavailable, Ki still starts with the Windows-only PowerShell tool and omits Bash-dependent tools.

## Run

```bash
# dev loop: build + run in a tmux session; listens on all IPv4 interfaces by default
# open the WebUI with the host's LAN IP, or pass --addr to narrow the listener
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

# live daemon (requires ki serve already running)
./ki reload
./ki extension list
./ki provider login <provider>
./ki provider logout <provider>
```

API auth is a Bearer token from `~/.ki/server.json` (or `KI_HOME/server.json`) for CLI clients. The WebUI asks for that token once and exchanges it for a short-lived HttpOnly browser session; the token is not embedded in HTML or URLs. Config is `~/.ki/ki.toml` and `<cwd>/.ki/ki.toml`. The configured real provider is used by default; set `KI_FAKE=1` only for local plumbing tests.

## Test

```bash
go test ./...
go test ./e2e
cd web && bun run test:e2e
cd web && bun run test:perf
cd web && bun run test:e2e:live
go test -tags live -timeout 5m ./e2e -run Live
```

Live tests call DashScope `qwen3.7-plus` (`dashscope-cn`). Put the key in `~/.ki/ki.toml` or `DASHSCOPE_CN_API_KEY`.
