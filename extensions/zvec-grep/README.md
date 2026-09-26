# zvec-grep

`zvec-grep` is a global Ki sidecar that registers one tool, `zvec_grep_search`,
plus the `/zg-index`, `/zg-status`, and `/zg-remove` commands. It wraps
[`@zvec/zvec-grep`](https://github.com/zvec-ai/zvec-grep) 0.2.1: a local hybrid
retrieval layer (lexical BM25 + vector similarity, fused and ranked) over a
workspace index stored in `<root>/.zvec-grep/`.

It deliberately does **not** replace the built-in search tools:

- exact lookups — a known word, quotation, name, date, key, filename, or regex —
  stay with `Grep` / `Glob`, which are exhaustive and cost nothing;
- `zvec_grep_search` answers questions whose wording or location is unknown:
  semantic, fuzzy, relationship, chronology, causality, comparison, and
  cross-file synthesis. It returns a ranked sample with `file:line` anchors so
  the model can follow up with `Read` or `Grep`.

The routing rules reach the model through `prompt/APPEND.md`
(`capabilities: ["prompt.append"]`), so they travel with the extension instead
of being hard-coded in the host.

## Requirements

- Node 22 or newer (the library's `engines` field; Ki's sidecar runs it with
  `bun`, which is already a Ki prerequisite).
- Disk space for the index and the embedding model. The default
  `local/potion-code-16m-v2` model is small (tens of KiB) and is downloaded on
  first use; larger local models and remote models are available through
  zvec-grep's own configuration.
- `runtime.install` (`bun install && bun run build`) runs on install: the
  library ships prebuilt native artifacts, so the dependency tree is installed
  rather than bundled.

## Install

Copy this directory to `{KI_HOME}/extensions/zvec-grep` and let Ki run the
manifest's `runtime.install`, or do it by hand:

```bash
cd "$KI_HOME/extensions/zvec-grep"
bun run setup   # bun install && bun run build
```

`{KI_HOME}/extensions/zvec-grep/node_modules/.bin` is declared in
`runtime.path`, so once the package is enabled the `zg` CLI is also on the PATH
of Bash and PowerShell commands (after Ki's bundled rg/fd, before the user's
PATH).

## Commands

| Command | Effect |
| --- | --- |
| `/zg-index [root] [--rebuild] [-g GLOB] [-t TYPE]` | Starts `zg index` in the background. Progress and completion appear on the extension status chip; option tokens are forwarded to `zg index`, and the `embedding`/`device` settings are forwarded ahead of them unless the command supplies its own `--embedding`/`--device`. |
| `/zg-status [root]` | Prints the index status (and any running index job). |
| `/zg-remove [root]` | Deletes the index after an explicit confirmation. |

Indexing is intentionally CLI work, never a tool call: the first run may
download a model, which is unbounded work far beyond the host's 120s tool
budget. `zvec_grep_search` therefore returns an actionable error when a
workspace has no index — it never builds one behind the user's back.

## Settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `embedding` | `local/potion-code-16m-v2` | Embedding model for queries and for the indexes `/zg-index` builds. |
| `device` | `auto` | Device for local embedding models; forwarded to `/zg-index` too. A remote model ignores it, because zg rejects a device outside `local/*`. |
| `mode` | `in-process` | `in-process` loads the library inside the sidecar; `external-daemon` shells out to `zg` and requires a running `zg --server`. |
| `maxResults` | `5` | Item limit used when the model does not pass `limit`. |
| `ignoredGlobs` | `[]` | Path rules excluded from every search, on top of any rule the model supplies. |
| `searchTimeoutMs` | `90000` | Sidecar-side search budget, kept below the host's hard limit. |
| `notifyOnIndexComplete` | `false` | Delivers a short message to the session when a background index job finishes. |

Credentials are deliberately **not** in this schema: the model never sees an API
key, and remote embedding providers are configured in zvec-grep's own store:

```bash
zg config provider set qwen --api-key "$DASHSCOPE_API_KEY"
zg config model set qwen/text-embedding-v4 --default
zg auth grant <root> --capability embedding --scope workspace
```

An index keeps the model it was built with. `/zg-index` forwards the `embedding`
(and `device`) setting, so switching the setting and running `/zg-index` on an
existing index is refused by zg with a rebuild hint; add `--rebuild` to build the
existing index with the new model. Pass `--embedding`/`--device` on the command
to override the setting for one run.

## Execution modes and concurrency

`mode: in-process` is the default and the recommended path: the sidecar holds
one library instance per effective configuration, refreshes changed files
before a search (`freshness: wait_for_fresh`, the default), and bounds
concurrent searches to four so one slow call cannot stall the sidecar.

An auto-update keeps zvec-grep's workspace write lock for its whole refresh, and
zg's read path refuses to run while a writer holds that lock, so the sidecar
serializes its searches per root: a second search waits for the first instead of
failing with "another writer holds the index write lock". Two further situations
degrade instead of failing: a refresh that loses the race for the index write
permit is retried once, then the search answers from the index as built (the
result then starts with `note: refresh skipped — …`, and
`details.refreshSkipped` records why); a search issued while `/zg-index` builds
the same root fails immediately naming that job, because the build owns the write
lock until it ends.

`mode: external-daemon` runs `zg query` against a daemon you manage. It fails
fast when `zg server status --check-ready` fails instead of starting a daemon
itself. Do not run `zg --server` as a daemon and the in-process engine against
the same workspace at the same time: zvec-grep serializes index writers with a
lease, and mixing the two means both sides load their own embedding model.

Searches are single calls into the library, which exposes no cancellation, so a
cancelled tool call (host abort or sidecar timeout) reports the cancellation and
cannot return partial rankings. `Grep` remains the fallback for an exact anchor.

## Result shape

`zvec_grep_search` answers with compact text, not JSON: a status header
(`freshness`, `root`, `source`, `coverage`, `items`, active routes), then one
block per ranked item with `path:startLine-endLine`, how it matched
(`matchedBy`, `score`, query groups), a bounded source preview with line
numbers, and — with `preview: full` — the item's outline.

Item-level `status: possibly_stale` means the index has not caught up with a
recent edit; the model is told to verify those files with `Grep` before making
claims about their current contents. A leading `note: refresh skipped — …` means
the search answered from the index as last built because the write lock was busy;
`details.refreshSkipped` names the reason (`write_busy` or `index_job`). Add
`.zvec-grep/` to the project's `.gitignore`.

## Tests

```bash
bun run test                  # build + protocol tests against a stubbed library and CLI
KI_ZVEC_GREP_LIVE=1 bun test  # also index a temp workspace and search it for real
```

The live test needs this package's dependencies installed (`bun run setup`) and
network access for the first model download; it keeps its global zvec-grep state
in a temp directory, so it never touches `~/.zvec-grep`.

## Troubleshooting

- **"No zvec-grep index for <root>"** — the workspace is not indexed. Run
  `/zg-index`; the model is told to ask you instead of building one itself.
- **`zg CLI not found`** — `bun run setup` has not run in this package.
- **Index stays "Preparing embedding model …"** — the first run is downloading
  the model; later runs reuse the cache.
- **`external-daemon` mode reports the daemon is not ready** — start it with
  `zg server on`, or switch the setting back to `in-process`.
- **`note: refresh skipped — …` on a result** — the index write lock was held, so
  the ranking comes from the index as last built (the header's `freshness` still
  reports what the library observed). Retry, or ask for `freshness: eventual` to
  request that behaviour deliberately.
- **"another writer holds the zvec-grep workspace write lock"** — another process
  (an external `zg index`, a `zg server`, or another ki session) is writing that
  index. zg's read path refuses to run alongside a writer, so the search retries
  briefly and then reports the owner; run it again once that writer finishes.
- **"cannot read the index … while an index job is building it"** — `/zg-index`
  owns the write lock for the whole build. Wait for the job (its progress is on
  the extension status chip) and search again.
- **"a zvec-grep daemon owns the index writes"** — a `zg server` daemon holds
  that root. Stop it with `zg server off`, or switch this extension's `mode`
  setting to `external-daemon` so searches go through the daemon.
