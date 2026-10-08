# Ki

An extensible agent runtime designed for easy integration with other applications.

## Install

Latest release, Linux amd64 / macOS arm64:

```sh
curl -fsSL https://raw.githubusercontent.com/skyw8/ki/main/scripts/install.sh | sh
```

Latest release, Windows amd64 (PowerShell):

```powershell
irm https://raw.githubusercontent.com/skyw8/ki/main/scripts/install.ps1 | iex
```

Both resolve the latest release, verify the archive against the release
`checksums.txt`, and install `ki` into `~/.local/bin` (Linux/macOS) or
`%LOCALAPPDATA%\Programs\Ki` (Windows), which the Windows script also adds to the
user PATH. Set `KI_VERSION` (`KI_VERSION=0.0.3`) to pin a release and
`KI_BIN_DIR` to install elsewhere. Only the archives the release matrix builds
are covered — Linux amd64, macOS arm64, Windows amd64 — so any other platform
has to [build from source](#build-from-source); the same archives are attached to
each [release](https://github.com/skyw8/ki/releases) for a manual install, where
a macOS archive that a browser (rather than `curl`) downloaded needs
`xattr -d com.apple.quarantine /path/to/ki` once before it runs.

## Build from source

`web/dist` is build output and is not tracked by git, so build the SPA first and
compile with the `embed` tag to get the single binary with the WebUI:

```bash
cd web && bun install && bun run build
cd .. && go build -tags embed -o ki ./cmd/ki
```

Without `-tags embed`, `go build ./cmd/ki` produces the CLI/API only; `ki serve`
then reports the UI as not built. `scripts/run.sh` hashes the frontend inputs and
reuses an unchanged `web/dist`; pass `--force-web` to rebuild it explicitly.
The script always compiles with `-tags embed`.

Windows builds automatically link the Ki application icon from the checked-in
`cmd/ki/rsrc_windows_{arch}.syso` resources. The icon is generated from the same
`web/src/assets/ki.svg` artwork used by the browser tab: render the SVG to
`cmd/ki/ki.ico`, then run `rsrc -ico cmd/ki/ki.ico -arch amd64|arm64 -o
cmd/ki/rsrc_windows_{amd64,arm64}.syso`. Plain command-line binaries on Linux and
macOS do not carry a file-manager application icon.

On Windows, Ki looks for Git Bash through `KI_GIT_BASH_PATH`, `CLAUDE_CODE_GIT_BASH_PATH`, standard Git for Windows locations, and then `bash.exe` on `PATH`. The unified `exec_command` tool defaults to PowerShell on Windows (preferring pwsh), then falls back to Git Bash. `write_stdin` continues an existing terminal; Unix PTY and Windows ConPTY support interactive input.

## Run

```bash
# dev loop: build + run in a tmux session; listens on all IPv4 interfaces by default
# open the WebUI with the host's LAN IP, or pass --addr to narrow the listener
scripts/run.sh

# open the WebUI (starts a detached server and opens an auto-login link);
# a double-click reaches this same path on Windows and macOS (via Terminal),
# while a Linux desktop needs a .desktop entry because file managers do not
# run ELF binaries directly
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

# browse and diagnose saved sessions (read-only; no server is started)
./ki session list --limit 20
./ki session search "apply_patch"
./ki session show <id> --view compact
./ki session trace <id> --cache-miss --context 1
./ki session inspect <id> --cache
# every browse command supports --format text|json|jsonl (--json/--jsonl aliases)

# inspect config and version
./ki config path
./ki version

# live daemon (requires ki serve already running)
./ki reload
./ki extension list
./ki provider login <provider>
./ki provider logout <provider>
```

API auth is a Bearer token from `~/.ki/server.json` (or `KI_HOME/server.json`) for CLI clients. Bare `ki` (including double-click launch) prints and opens an auto-login link with that token in the URL fragment (`#token=…`). The WebUI immediately removes the fragment and exchanges the token for a short-lived HttpOnly browser session; no extra login is needed. Treat this link as a secret and do not share it. Plain WebUI URLs still offer manual token login. The token is never embedded in HTML or a URL query. Config is `~/.ki/ki.toml` and `<cwd>/.ki/ki.toml`. The configured real provider is used by default; set `KI_FAKE=1` only for local plumbing tests.

## Bundled extensions

The extension protocol accepts sidecars written in any language. Bundled extensions
are Go executables, with a Rust exception for `zvec-grep` and its native search engine.
Each extension is compiled separately. Ki launches its executable as an external
NDJSON JSON-RPC child process; it links or embeds no extension runtime or payload.
The build script and optional installer also run outside the host process.

```bash
go run ./scripts/build-extensions.go
# Or build only selected Go extensions:
go run ./scripts/build-extensions.go -only goal,telegram-bot
```

Copy a staged `var/extensions/<name>` directory into `{KI_HOME}/extensions/<name>`.
The default package launches its binary directly and needs no source files or
language toolchain. To distribute sources that build their executable on first
launch, use `go run ./scripts/build-extensions.go -source -out var/extensions-source`
(optionally `-only`), then copy `var/extensions-source/<name>` into the extension
installation directory.
These packages work outside the checkout: each includes a standalone Go module
and only the shared Ki sources it needs. All bundled manifests run
`go run ./install/main.go` only when `bin/<name>` is missing; an existing binary
always skips installation. A missing binary in a source-free package requires
reinstalling the binary package or replacing it with a source package.
Building `zvec-grep` needs its native Rust/C++ prerequisites;
see [its README](extensions/zvec-grep/README.md). Its Rust index format requires
an explicit rebuild of existing JavaScript indexes. See [the extension contract](docs/extension.md)
for packaging and protocol details.

## Test

```bash
(cd web && bun run typecheck && bun run build)
# The default CI command omits standalone bundled sidecar packages:
go test -tags embed -count=1 $(go list ./... | rg -v '^ki/extensions/')
# Include bundled sidecars when changing one:
go test -tags embed -count=1 ./...
# Focused development checks; no need to repeat these after the full run passes:
go test ./internal/... ./pkg/...
(cd web && bun run test:unit)      # pure logic; test:unit:watch for continuous feedback
(cd web && bun run test)           # all WebUI unit + browser tests
(cd web && bun run test:perf)
(cd web && bun run test:e2e:live)
go test -tags live -timeout 5m ./e2e -run Live
```

Keep `cd web && bun run typecheck:watch` running while editing. TypeScript's
incremental cache lives in `web/node_modules/.cache/ki/`. `test:e2e` runs only the
browser suite; `test:e2e:serial` is its single-process debugging fallback. The
full Go run requires the WebUI dependencies and Chromium to include web tests.

Live tests call DeepSeek `deepseek-flash` over all three wire protocols
(Completions, Responses, Anthropic). Put the key in `~/.ki` (credentials or
`ki.toml`) or `DEEPSEEK_API_KEY`.

## CI and releases

GitHub Actions runs formatting, vet, WebUI type/build checks, core Go tests on
Linux, macOS, and Windows, the complete fake-model Playwright suite, screenshot
coverage, and the long-history performance suite. Standalone bundled sidecar
package tests, extension transport races, and native `zvec-grep` checks are
opt-in: use the `CI` workflow's manual dispatch and enable `extensions` when
changing an extension. Host extension runtime and contract tests remain in the
core suite. Configure branch protection for `main` to require the `CI` workflow
checks before merging.

Releases are created only from semantic-version tags. The default release does
not need provider credentials; add `DEEPSEEK_API_KEY` only if you manually run
the opt-in live CI suite. Push an annotated tag:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The release workflow reruns the default CI gate (excluding both credentialed live
tests and bundled extension checks) against that exact tag. Only after the tests
pass does it build the Linux amd64, macOS arm64, and Windows amd64 archives,
inject the tag's version (the tag without its `v` prefix, matching the checked-in
`internal/cli/version.go`) into `ki version`, generate SHA-256 checksums, and
publish the GitHub Release. Extension packages can still be built separately
with the commands in [Bundled extensions](#bundled-extensions).
