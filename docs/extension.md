# 扩展（Extensions）

包是带 `extension.json` 的目录，发现路径：

- 全局：`{KI_HOME}/extensions/<name>/`
- 项目：`<cwd>/.ki/extensions/<name>/`

同名时项目覆盖全局。启用开关是进程级 `{KI_HOME}/toggles.json` 的 `extensions.disabled`（缺省 = 全开）。设置 API：`GET/PATCH /v1/extensions`（形态对齐 skills/MCP）。Session GET 带 `availableExtensions`。禁用的包仍列出（`enabled: false`），但不贡献、不拉起 sidecar。目录列表按名字排序；intercept / prompt 链序是剩余的「全局按名 → 项目按名」（`Enabled` / `Discover.Enabled`，不要直接喂名字排序后的 `All`）。

## 支持的能力

`extension.json` 的 `capabilities[]` 可声明下列能力：

| 能力 | 类型 | 作用 |
|---|---|---|
| `prompt.append` | 声明式 | 在用户 `APPEND_SYSTEM.md` 之后追加一层 system prompt |
| `skill` | 声明式 | 额外 skill 根目录（`SKILL.md` 树） |
| `command` | 声明式 + 可选代码 | `commands/*.md` 作 slash 模板（`KindTemplate`）；若有 sidecar，还可在 `initialize` 里登记可执行 slash |
| `mcp` | 声明式 | 内联 `mcp.mcpServers`（`ServerSpec`），合并进 `Snapshot.MCP`，由现有 `mcp.Manager` 拉起；**本包不实现 MCP 协议** |
| `tool` | 代码（sidecar） | 自定义工具；模型侧裸名；经 `tool.execute` 调用（不是 MCP） |
| `hook` | 代码（sidecar） | 异步、只读；Host 推送脱敏后的 `event`（notify，不拦主路径） |
| `intercept` | 代码（sidecar） | 同步、可改控制流；须在 `intercept[]` 声明切入点 |

`intercept[]` 当前支持的切入点：

| 切入点 | 含义 |
|---|---|
| `tool` | 工具调用前后：可 `{block}` / 改 args 或 result（内置与 MCP 工具同一路径） |
| `provider` | 模型请求路径：`beforeRun` / `transformContext` / `request`（可 short-circuit）/ `error` fallback |
| `provider.http` | 仅 headers / URL（`Authorization`、`X-Api-Key`、`Cookie` 会剥掉；空值表示删除该 header） |

未声明的切入点不会被调用。

## 两类贡献方式

**声明式（可不启 sidecar）**：`prompt.append`、`skill`、markdown `command`、`mcp`。MCP server 名冲突时用户 `.mcp.json` 获胜，且用户 MCP 工具名不加前缀。扩展贡献的 MCP 工具名是 `{extension.json 的 name}/{wireName}`；`CallTool` 仍用 `WireName`。

**代码（sidecar）**：每个已启用且声明了 `tool` / `hook` / `intercept`，或需要可执行 slash 的包，最多一个 NDJSON JSON-RPC 2.0 sidecar（`runtime.kind=rpc`）。不链进 ki 二进制。`hook` 异步；`intercept` 同步。自定义工具走 `tool.execute`。登记在 Reload 前冻结。

## Intercept 与其它行为

工具 intercept 可对内置与 MCP 工具 `{block}` / 改写 args 或 result（改写前先校验）。sidecar 失败默认 fail-open；若 `failClosed`：BeforeTool → block，BeforeProvider → 罐头 `StopReason=stop` + `text_delta`。同一轮 occupy 内失败的拦截器会进入共享 skip 集（hook 与 provider 共用），后续不再调用。Provider intercept 只包住 live occupy 的 Streamer；compact 看不到 BeforeProvider。HTTP intercept 只动 headers/URL。Live Stream 仍看到原始（未脱敏）messages，除非 intercept 改写了它们。`KI_FAKE` / 注入的 Scripted 不变。可执行 slash 在 busy 时 409；用户/home 的 `prompts/*.md` 优先于扩展 handler。`initialize` 返回的 tools/commands 若未声明对应 `tool`/`command` 能力，会丢弃并发 `extension_error`。

Reload / abort 会杀 sidecar 进程组（`tools.AttachProcessGroup` / `KillProcessGroup`，不是 MCP connect）。`extension_error` 是与 MCP 失败同类的 sideband 事件。

实现包：`internal/extension`。Host 侧 `Interceptor` 接口仅供 `rpcClient` 与测试使用。
