# 扩展（Extensions）

包是带 `extension.json` 的目录。声明式贡献并入现有 Snapshot 子系统；需要跑代码时只走 **NDJSON JSON-RPC 2.0 sidecar**（语言无关，不编进 ki）。实现包：`internal/extension`。Host 侧 `Interceptor` 仅供 `rpcClient` 与测试使用。

## 发现与开关

| 位置 | Scope |
|---|---|
| `{KI_HOME}/extensions/<name>/` | 全局（`home`） |
| `<cwd>/.ki/extensions/<name>/` | 项目（`project`） |

- `name` 是主键：同名时项目覆盖全局。`name` 须匹配 `^[a-z0-9][a-z0-9-]{0,62}$`，禁止 `ki.` 前缀。
- 启用开关是进程级 `{KI_HOME}/toggles.json` 的 `extensions.disabled`（缺省空 = 全开）。无 trust。
- 禁用的包仍出现在列表（`enabled: false`），但不贡献、不拉起 sidecar / 扩展 MCP。
- 目录列表按名字排序（`Discover.All`）。**链序**（prompt 追加 / intercept）是剩余的「全局按名 → 项目按名」（用 `Enabled` / `Discover.Enabled`，不要直接喂 `All`）。

## 包布局

```
my-ext/
├── extension.json          # 必填
├── prompt/APPEND.md        # 可选：prompt.append
├── skills/…/SKILL.md       # 可选：skill
├── commands/*.md           # 可选：slash 模板
└── bin/extension           # 可选：sidecar 可执行文件
```

单文件扩展不支持。路径相对包根，禁止 `..` 逃逸。

## extension.json

```json
{
  "name": "protected-paths",
  "version": "0.1.0",
  "description": "…",
  "capabilities": ["prompt.append", "skill", "command", "mcp", "hook", "tool", "intercept"],
  "intercept": ["tool", "provider", "provider.http"],
  "failClosed": false,
  "prompt": { "append": ["prompt/APPEND.md"] },
  "skills": ["skills"],
  "commands": ["commands"],
  "mcp": { "mcpServers": { "time": { "command": "uvx", "args": ["mcp-server-time"] } } },
  "runtime": { "kind": "rpc", "command": "bin/extension", "args": [], "env": {} }
}
```

- `capabilities`：**门闸**。未声明的能力：对应字段忽略；`initialize` 返回的 tools/commands 丢弃并 `extension_error`。未知字符串 warning、不失败。
- `intercept[]`：仅当含 `intercept` 能力时生效；未列入的切入点不调对应 RPC。声明了 `intercept` 却没有已知点 → 加载错误。
- `failClosed`：缺省 `false`。见下方失败策略。
- `runtime.kind`：`none`（缺省）| `rpc`。`rpc` 须声明 `tool` / `hook` / `intercept` / `command` 之一；代码能力缺 `rpc` 则丢弃并报错。无 `inprocess`。

## 支持的能力

| 能力 | 类型 | 作用 |
|---|---|---|
| `prompt.append` | 声明式 | 用户 `APPEND_SYSTEM.md` 之后追加 system 层（第 6 层，见 [system_prompt.md](system_prompt.md)） |
| `skill` | 声明式 | 额外 skill 根目录 |
| `command` | 声明式 + 可选代码 | `commands/*.md` 作 slash 模板；sidecar 还可在 `initialize` 登记可执行 slash |
| `mcp` | 声明式 | 内联 `mcpServers`，合并进 `Snapshot.MCP`，由 `mcp.Manager` 拉起；**本包不实现 MCP** |
| `tool` | sidecar | 自定义工具；模型侧裸名；经 `tool.execute` |
| `hook` | sidecar | 异步只读；Host 推送脱敏 `event` |
| `intercept` | sidecar | 同步、可改控制流；须在 `intercept[]` 声明切入点 |

`intercept[]` 切入点：

| 点 | 含义 |
|---|---|
| `tool` | 工具前后：`{block}` / 改 args 或 result（内置与 MCP 同一路径） |
| `provider` | `beforeRun` / `transformContext` / `request`（可 short-circuit）/ `error` fallback |
| `provider.http` | 仅 headers / URL（剥掉 `Authorization`、`X-Api-Key`、`Cookie`；空值删 header） |

## 贡献方式

作者只有两条路，都不把扩展编进 ki：

| 方式 | 写什么 | Host 做什么 |
|---|---|---|
| **声明式** | `prompt.append` / `skills/` / `commands/*.md` / 内联 `mcpServers` | 合并进 Snapshot；**不起** sidecar |
| **代码** | `runtime.kind=rpc` + 可执行文件 | 每个启用的包 **最多起一个** sidecar，同时承载 tool / hook / intercept / 可执行 slash |

## 运行时进程

扩展相关进程只有这三类，不要混称「扩展」：

| 进程 | 所有者 | 协议 | 何时出现 |
|---|---|---|---|
| **ki Host** | `ki serve` | 内部 | 始终 |
| **sidecar** | `extension.Manager` | NDJSON JSON-RPC（非 MCP） | 该包启用且 `runtime.kind=rpc` |
| **MCP server** | `mcp.Manager` | MCP | 用户 `.mcp.json` 或扩展 `mcpServers` 被启用 |

- 纯声明式包（只有 prompt/skills/markdown command）→ 只有 Host。
- 只贡献 MCP、无 `runtime` → Host + MCP 子进程，**没有** sidecar。
- 有代码能力 → Host + 该包一个 sidecar；若同时贡献 `mcpServers`，再加 N 个 MCP 进程。
- **自定义工具 ≠ 扩展 MCP**：`tool.execute` 走 sidecar，模型见裸名；扩展 MCP 是独立进程，模型见 `{extensionName}/{wireName}`，其它 MCP host 也能用。一个包可以两种都贡献。

## 合并与冲突

**Prompt：** 按链序拼接，每段 `<extension_instructions name="…">`。单文件 64 KiB，总和 256 KiB，超出截断。动态改 system **只**走 `intercept.provider.beforeRun`（每 occupy 一次，看不见后续 steer）；`BeforeProvider` 禁止改 `System`。

**Skills 根插入顺序**（`seen[name]` 先到先得；不改变 home 赢过 project）：`{KI_HOME}/skills` → `~/.agents/skills` → `<cwd>/.ki/skills` → **项目扩展** → **全局扩展** → 祖先 `.agents/skills`。

**Slash：**

1. builtin `compact` / `reload` 保留。
2. 用户/home `prompts/*.md`（无 `Extension`）赢过扩展模板与扩展 handler。
3. 扩展 `CommandSpec`（`source=extension`）赢过同名扩展 markdown。
4. 可执行 slash busy 时 409（在 invoke 前）；超时 15s。

**MCP：**

- server 名：用户 `.mcp.json` 赢过任何扩展；扩展之间先到先得（链序），后者 skip。
- 工具名：扩展贡献强制 `{extension.json name}/{wireName}`；用户 `.mcp.json` 不加前缀。`CallTool` 仍用 `WireName`。`myext/Read` 与内置 `Read` 允许并存。

**自定义工具装配：** builtin → 扩展 sidecar（裸名）→ MCP。精确匹配保留名（`Read`/`Write`/`Edit`/…）的 sidecar 工具拒绝。冲突 skip + `extension_error`，无 `:1` 后缀。

## Sidecar 协议

分帧：**NDJSON**（一行一个 JSON-RPC 2.0 对象），不是 MCP `Content-Length`。stderr 不解析。

环境：不继承完整 `os.Environ()`。注入 `KI_EXTENSION`、`KI_SESSION_ID`、`KI_CWD`、`KI_HOME`、`KI_EXTENSION_ROOT`，外加少量平台必需变量，以及 `runtime.env` 白名单。

超时：`initialize` 10s；拦截 2s；`tool.execute` 120s（或 `ToolSpec.timeoutMs`）；`command.invoke` 15s。超时/取消发 `cancel` 通知并丢弃迟到 result。

| method | 门闸 | 说明 |
|---|---|---|
| `initialize` | — | params：`sessionId/cwd/home/extensionRoot/capabilities`；result：`{tools,commands,fallback}`，此后冻结 |
| `shutdown` | — | 关闭 |
| `tool.execute` | `tool` | 进度：sidecar→Host 通知 `tool.progress`（`id` = 该请求 id） |
| `command.invoke` | `command` | `{handled,notice,prompt}`；`handled=false` 且 `prompt` 非空则落入 occupy |
| `intercept.provider.beforeRun` | 点 `provider` | 可改 system / messages |
| `intercept.provider.transformContext` | 点 `provider` | 仅 messages（attachments 已物化） |
| `intercept.tool.before` / `.after` | 点 `tool` | before 可 block；Validate 在 before **之前** |
| `intercept.provider.request` | 点 `provider` | 可改 Messages/Tools/Model/…；不可改 API/System；可 `{shortCircuit:{text}}` |
| `intercept.provider.http` / `.http.after` | 点 `provider.http` | 无 body |
| `intercept.provider.error` | 点 `provider` 且 `fallback` | 非空 text → canned `stop` |
| `event`（通知） | `hook` | 脱敏 DTO，无返回 |
| `cancel`（通知） | — | `{id}` |

`Registration` 在 `initialize` 后冻结，直到 Reload。只声明点 `tool` 的包看不到 system / messages。

## Intercept 行为

- 工具 intercept 对内置与 MCP 同样生效；改写前已过 schema。先 block 胜出；全局 block 后项目看不到该 call。
- Provider intercept **只**包 live occupy 的 Streamer；compact 只用 session `HTTPDoer`，看不见 BeforeProvider。
- Short-circuit / failClosed BeforeProvider / fallback 成功：返回 `(asst, nil)` 且 `StopReason=stop`，并已 `text_delta`（对齐 `streamWithRetry`）。
- Live Stream 默认仍见原始 messages（除非 intercept 改写）。`KI_FAKE` / 注入 Scripted 不走 `Resolve`+`NewLiveModel`。
- HTTPDoer **按 session** 注入 `NewLiveModel`，不挂进程级 `router`。

## Hook 事件

声明 `hook` 的 sidecar 在 persist/SSE **之后异步**收 `event`，不拦主路径。DTO 无 prompt / args / result / system。

投递：`agent_*`、`turn_*`、`message_start`/`message_end`、`request_header`、`tool_execution_start`/`end`、`compaction_*`，以及部分 server/MCP sideband。

**不投递：** `message_update`、`tool_execution_update`、`context_usage`、`patch_apply_updated`、`extension_error`（不回投肇事扩展）。

## 失败策略

扩展层默认 **fail-open**：RPC/panic/超时 → `extension_error`，本 occupy 内该扩展进共享 skip 集（hook 与 provider 共用），其它扩展继续，工具放行。Manager **从不**把扩展失败变成 `BeforeTool` 的 `error`（否则 loop 会 fail-closed）。

`failClosed: true` 仅两条同步入口：BeforeTool → 合成 `{block}`；BeforeProvider → canned `stop`。AfterTool / TransformContext / OnEvent 仍 fail-open。

`extension_error` 是与 MCP 失败同类的 sideband（`AppendSidebandEvent` + SSE toast）。

## 生命周期

1. `Loader.scan` 锁内 **只读** Discover（不 exec）。
2. `runPrompt`：`Prepare` sidecar 与 MCP 握手并行；本轮 tools = builtin + 扩展工具 + MCP。
3. Steer 不重新 Prepare。
4. Reload / abort / Close：杀 sidecar **进程组**（`tools.AttachProcessGroup` / `KillProcessGroup`，不是 MCP connect）。活跃 session 的 Reload 排到 occupy `release` 之后。

## API

| 方法 | 路径 | 作用 |
|---|---|---|
| `GET` / `PATCH` | `/v1/extensions?workspaceId=` | Scan 列表 / 整表替换 `disabled` 后 `Reload()`（对齐 skills/MCP） |
| `GET` | `/v1/sessions/{id}` | `availableExtensions`；`commands[].extension` |
| `POST` | `/v1/reload` | 额外关 sidecar |

设置页只 Scan、不 Prepare。`path` 只展示字符串，不当 `href`。不新增 session 级 extensions 路由。
