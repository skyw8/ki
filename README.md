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
then reports the UI as not built. `scripts/run.sh` rebuilds `web/dist` on every
run and always uses `-tags embed`.

Windows builds automatically link the Ki application icon from the checked-in
`cmd/ki/rsrc_windows_{arch}.syso` resources. The icon is generated from the same
`web/src/assets/ki.svg` artwork used by the browser tab: render the SVG to
`cmd/ki/ki.ico`, then run `rsrc -ico cmd/ki/ki.ico -arch amd64|arm64 -o
cmd/ki/rsrc_windows_{amd64,arm64}.syso`. Plain command-line binaries on Linux and
macOS do not carry a file-manager application icon.

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
cd web && bun run test:e2e         # parallel runner; test:e2e:serial for one process
cd web && bun run test:perf
cd web && bun run test:e2e:live
go test -tags live -timeout 5m ./e2e -run Live
```

Live tests call DashScope `qwen3.7-plus` (`dashscope-cn`). Put the key in `~/.ki/ki.toml` or `DASHSCOPE_CN_API_KEY`.

## CI and releases

GitHub Actions runs formatting, vet, WebUI type/build checks, Go tests on Linux,
macOS, and Windows, the complete fake-model Playwright suite, screenshot coverage,
and the long-history performance suite. Configure branch protection for `main`
to require the `CI` workflow checks before merging.

Releases are created only from semantic-version tags. Add
`DASHSCOPE_CN_API_KEY` as a GitHub Actions repository secret, then push an
annotated tag:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The release workflow reruns every CI test plus the credentialed live-provider
CLI/WebUI suite against that exact tag. Only after all tests pass does it build
Linux, macOS, and Windows archives for amd64 and arm64, inject the tag's version
(the tag without its `v` prefix, matching the checked-in `internal/cli/version.go`)
into `ki version`, generate SHA-256 checksums, and publish the GitHub Release.
