# zvec-grep

`zvec-grep` is a Ki extension that registers `zvec_grep_search` and the
`/zg-index`, `/zg-status`, and `/zg-remove` commands. Its sidecar is written in
Rust, using the native [zvec-grep engine](https://github.com/zvec-ai/zvec-grep/tree/c1d2297ccd815af9305920943a1e0f08ae502d51/rust)
pinned at `c1d2297ccd815af9305920943a1e0f08ae502d51`. The engine combines lexical
BM25 and vector similarity with rank fusion over `<root>/.zvec-grep/`.

Exact words, names, paths, keys and regular expressions stay with Ki's exhaustive
`Grep` and `Glob` tools. Semantic and cross-file questions use this extension's
ranked results, with `file:line` anchors for follow-up `Read` or `Grep` calls.
The routing guidance is supplied by `prompt/APPEND.md`.

## Build and install

Runtime packages contain `extension.json`, `bin/zvec-grep` (`.exe` on Windows),
`bin/zg`, the prompt and locales. Both executable names contain the same bytes.
The extension starts from one executable without source files, Node, Bun, Cargo,
CMake, or running its installation hook. `bin/zg` dispatches the management CLI and is
added to Ki's tool PATH by `runtime.path`.

The executable embeds its native engine, zvec shared library, tokenizer
resources, and any shared model runtime libraries. On first launch it extracts
these into a private, content-addressed directory under
`{KI_HOME}/cache/extensions/zvec-grep/`, then executes the engine there. Standalone
CLI use without `KI_HOME` uses the user's cache directory. Publication is atomic,
so concurrent launches do not load partial libraries. Model weights and workspace
indexes remain ordinary engine-managed data and may be downloaded on first use.

Build from this directory:

```bash
rustup toolchain install 1.98.0 --profile minimal
cargo build --release --locked
```

Building requires Rustup with Rust 1.98, Git, CMake, a C++ compiler and libclang.
The first build downloads pinned Rust dependencies and the native SDK assets.
On Debian/Ubuntu install `cmake clang libclang-dev g++`; on macOS use the Xcode
command-line tools and CMake; on Windows use MSVC's C++ build tools, CMake and
LLVM/libclang. `KI_ZVEC_GREP_BUILD_JOBS` controls native compilation concurrency
(default four). The launcher build invokes the native Cargo build and embeds
its complete runtime; no manual asset preparation is required.

The upstream native platform set is Linux x86-64/ARM64 with glibc, macOS
x86-64/ARM64, and Windows x86-64/MSVC. Build on the target host; the native model
SDKs require their own target toolchains for cross compilation. This migration
was verified on Linux x86-64. CPU inference is always available; an unavailable
requested accelerator falls back according to the upstream model backend.

Use Ki's repository packaging command to stage a complete installable package,
or copy the release executable to `bin/zvec-grep` and `bin/zg` with the manifest,
prompt and locales under `{KI_HOME}/extensions/zvec-grep`.

The native engine uses a different index format from the previous JavaScript
engine. Rebuild existing indexes explicitly with `/zg-index --rebuild`. Searches
never rebuild an incompatible or missing index automatically.

## Commands

| Command | Effect |
| --- | --- |
| `/zg-index [root] [--rebuild] [-g GLOB] [-t TYPE]` | Starts indexing in a background child process. Progress and completion appear on the extension status chip. Other index options are forwarded; the embedding/device settings precede them unless the command supplies its own flags. |
| `/zg-status [root]` | Reports the workspace index status and any running job. |
| `/zg-remove [root]` | Deletes the index after confirmation. |

The bundled CLI accepts the existing `zg index`, `zg status`, `zg query`,
`zg server`, `zg config` and `zg auth` command spellings as well as the native
engine's `--index`, `--status`, `--server`, `--config` and `--auth` forms. Indexing
runs outside tool calls because a first build can download an embedding model
and exceed the host's 120-second search limit.

## Settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `embedding` | `local/potion-code-16m-v2` | Model used for queries and `/zg-index` builds. |
| `device` | `auto` | Local-model device; remote embeddings omit the device. |
| `mode` | `in-process` | Native engine in the sidecar, or `external-daemon` to use a daemon the user manages. |
| `maxResults` | `5` | Default result limit. |
| `ignoredGlobs` | `[]` | Path exclusions applied to indexed searches independently of model-supplied scope. |
| `searchTimeoutMs` | `90000` | Sidecar search budget below the host hard limit. |
| `notifyOnIndexComplete` | `false` | Opt-in completion message delivered to the session. |

The host writes settings to the extension's `config.json`. `config.updated`
invalidates the cached configuration and engine. A newer state version is
rejected without changing the file; index management falls back to defaults
when configuration is unreadable.

Remote credentials remain in zvec-grep's own store and never appear in the tool
schema. For example:

```bash
zg config provider set qwen --api-key "$DASHSCOPE_API_KEY"
zg config model set qwen/text-embedding-v4 --default
zg auth grant <root> --capability embedding --scope workspace
```

An index retains its embedding model. Changing settings and updating an existing
index surfaces a rebuild hint; `/zg-index --rebuild` replaces it. Command-level
`--embedding`/`--device` flags override settings for that run.
Both search errors and failed index jobs name `/zg-index --rebuild` when the
engine requires a rebuild, including old index formats and model mismatches.
Plain `/zg-index` only updates a compatible index.

## Search behavior

The tool schema, defaults, locale resources, prompt and compact preview format
remain the same. A query can combine hybrid, lexical-only and vector-only groups,
fuse rankings, select short/full previews, and filter by indexed paths, file
types, symbols and modification times. Legacy scan options accepted by the tool
remain separate from persisted index scanning policy.

A status header reports freshness, root, source, coverage, item count and active
routes. Each ranked item includes its path and inclusive line range, match mode,
score, metadata and line-numbered source. Short previews retain up to 12 lines
and 1,000 characters; full previews retain up to 80 lines and 8,000 characters,
plus available outlines. Total output remains bounded to 24,000 characters.
The native engine's half-open source ranges are converted to inclusive anchors,
and source numbering uses the content's own coordinates.

The sidecar bounds concurrent searches to four and serializes searches of each
root to prevent its refreshes from colliding. Fresh searches refresh changed
files; `freshness=eventual` reads the index as last built and reports stale items.
A contended write permit is retried once, then refresh is skipped. Workspace
write-lock contention is retried twice before reporting its owner. Searches
alongside this sidecar's own index job skip refresh and report that job if the
index cannot be read. `details.refreshSkipped` records `write_busy` or
`index_job`, and the explanatory note preserves the observed freshness header.

Cancellation replies once immediately and signals native work cooperatively.
A timeout also signals cancellation and releases its slot. Neither can produce
a partial ranking. Closing host stdin drains pending replies and exits within
a two-second shutdown budget, including when stdout or native work is blocked.
A missing index names `/zg-index`; a disabled index tells the
model to use Grep/Glob and leaves re-enabling to the user.

`external-daemon` requires `zg server status --check-ready` to succeed and never
starts a daemon as a side effect of searching. Start one with `zg server on`, or
return the extension to `in-process` mode. Finished background jobs append the
same `zvec-grep-index` entry; optional completion notifications use the same
session queue and idempotency key. Success status clears after 30 seconds;
starting another index job cancels that expiry so it cannot erase newer progress
or a failure. Failure status remains until the next index job replaces it.
The unified extension dialog shows the full error above its details/config tabs
using the same plain red notice as chat messages.

## Tests

```bash
cargo build --release --locked
node --test test/*.test.mjs
cargo +1.98.0 test --release --locked --manifest-path native/Cargo.toml
KI_ZVEC_GREP_LIVE=1 node --test test/live.test.mjs
```

The protocol tests retain the original assertions for settings, routing,
previews, cancellation, lock retries, concurrent searches, commands, progress,
confirmation and notifications. Rust test engine and CLI seams keep
these deterministic. Regression tests also cover JavaScript-compatible settings
and limit coercion, whitespace handling, version headers and bounded EOF shutdown.
A standalone test copies only the executable and launches
it with an empty PATH, then checks initialization, missing-index search and
status, including a final reply after stdin closes. The opt-in live test also
copies only the executable, clears PATH and library search variables, and builds
and queries a real semantic index in an isolated temporary home. On Linux it
checks that zvec is loaded from the extracted runtime cache.
`KI_ZVEC_GREP_RUNTIME` selects a packaged executable
for the same tests.

## Source package fallback

To stage a source package that can build outside this checkout, run from the repository root:

```bash
go run ./scripts/build-extensions.go -source -out var/extensions-source -only zvec-grep
```

Copy `var/extensions-source/zvec-grep` to `{KI_HOME}/extensions/zvec-grep`. When `bin/zvec-grep` (`.exe` on Windows) is missing, Ki runs `go run ./install/main.go` at the package root. The package includes a standalone Go module and minimal shared Ki sources; it excludes private configuration, state, caches, and build output. Go is required for this first build. The installer also requires Cargo/Rust 1.98.0 and the native C++/CMake/libclang prerequisites above, then publishes the same self-contained launcher as both `bin/zvec-grep` and `bin/zg`. Once the binary exists, installation is skipped and no compiler or source files are needed to launch it. Default binary packages retain the same manifest metadata but omit sources and the installer; replace an incomplete binary package with a complete binary or source package.
