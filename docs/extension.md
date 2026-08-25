# Extensions

A package is a directory with `extension.json` under `{KI_HOME}/extensions/<name>/` or `<cwd>/.ki/extensions/<name>/`. Project same-name replaces global. Enablement is process-wide `{KI_HOME}/toggles.json` `extensions.disabled` (missing = all on). Settings: `GET/PATCH /v1/extensions` (same shape as skills/MCP). Session GET adds `availableExtensions`. Disabled packages stay listed (`enabled: false`) and do not contribute or spawn. Catalog listing is name-sorted; intercept/prompt chain order is remaining global-by-name then project-by-name (`Enabled` / `Discover.Enabled`, never bare name-sorted `All`).

## Two contribution kinds

Declarative (no sidecar): extra system-prompt layer after user `APPEND_SYSTEM.md`, extra skill roots, slash `commands/*.md` (`KindTemplate`), MCP server specs merged into `Snapshot.MCP`. User `.mcp.json` wins on server name and keeps unprefixed tool names. Extension MCP tools are `{extension.json name}/{wireName}`; `CallTool` uses `WireName`.

Code: one NDJSON JSON-RPC 2.0 sidecar per enabled package that declared `tool` / `hook` / `intercept` or executable slash (`runtime.kind=rpc`). Not compiled into ki. `hook` is async redacted `event`. `intercept` is sync; v1 points in `intercept[]` are `tool`, `provider`, `provider.http` (undeclared points are not invoked). Custom tools use `tool.execute`. Registration freezes until Reload.

## Intercept

Tool intercept can `{block}` / mutate args or result on builtin and MCP tools (validate before mutate). Sidecar failure is fail-open unless `failClosed` (BeforeTool → block; BeforeProvider → canned `StopReason=stop` + `text_delta`); a failed interceptor is skipped for the rest of that occupy (hooks and provider share one skip set). Provider intercept wraps live occupy Streamer only; compact never sees BeforeProvider. HTTP intercept is headers/URL only (`Authorization`, `X-Api-Key`, and `Cookie` stripped; empty header patch values delete). Live Stream still sees original (unredacted) messages unless the interceptor mutated them. `KI_FAKE` / injected Scripted is unchanged. Executable slash is 409 while busy; user/home `prompts/*.md` beat extension handlers. `initialize` tools/commands without `tool`/`command` are dropped with `extension_error`.

Reload/abort kills the sidecar process group (`tools.AttachProcessGroup` / `KillProcessGroup`, not MCP connect). `extension_error` is a sideband event like MCP failures.

Package: `internal/extension`. Host `Interceptor` is for `rpcClient` and tests only.
