# 扩展（Extensions）

包是带 `extension.json` 的目录。声明式贡献并入 Snapshot；需要跑代码时只走 **NDJSON JSON-RPC 2.0 sidecar**（语言无关，不编进 ki）。实现包：`internal/extension`。

**不做**旧 `hook` / `intercept` / `intercept[]` 协议。代码能力对外只订事件（`lifecycle`）+ 可选 inbound Host 方法。

## 发现与开关

| 位置 | Scope |
|---|---|
| `{KI_HOME}/extensions/<name>/` | 全局（`home`） |
| `<cwd>/.ki/extensions/<name>/` | 项目（`project`） |

- `name` 是主键：同名时项目覆盖全局。`name` 须匹配 `^[a-z0-9][a-z0-9-]{0,62}$`，禁止 `ki.` 前缀。
- 启用开关是进程级 `{KI_HOME}/toggles.json` 的 `extensions.disabled`（缺省空 = 全开）。
- 禁用的包仍出现在列表（`enabled: false`），但不贡献、不拉起 sidecar / 扩展 MCP。
- 目录列表按名字排序（`Discover.All`）。**链序**（prompt 追加 / sync 生命周期）是「全局按名 → 项目按名」（`Enabled` / `Discover.Enabled`）。

## 包布局

```
my-ext/
├── extension.json
├── prompt/APPEND.md
├── skills/…/SKILL.md
├── commands/*.md
└── bin/extension
```

路径相对包根，禁止 `..` 逃逸。

## extension.json

```json
{
  "name": "protected-paths",
  "version": "0.1.0",
  "description": "…",
  "capabilities": ["prompt.append", "skill", "command", "mcp", "tool", "lifecycle", "bus"],
  "failClosed": false,
  "prompt": { "append": ["prompt/APPEND.md"] },
  "skills": ["skills"],
  "commands": ["commands"],
  "mcp": { "mcpServers": { "time": { "command": "uvx", "args": ["mcp-server-time"] } } },
  "runtime": { "kind": "rpc", "command": "bin/extension", "args": [], "install": [], "env": {} }
}
```

- `capabilities`：门闸。未声明的能力：对应字段忽略；`initialize` 多报的 tools/commands 丢弃并 `extension_error`。
- `failClosed`：缺省 `false`。仅 **sync** 生命周期入口：`tool_call` 失败 → 合成 block；`before_provider_request` 失败 → canned stop。
- `runtime.kind`：`none`（缺省）| `rpc`。`rpc` 须声明 `tool` / `lifecycle` / `command` / `bus` 之一。
- `runtime.command`：与 MCP 相同。无路径分隔符（`node` / `npx`）走 **PATH**；带 `/` 的相对路径相对包根（`bin/extension`）；绝对路径原样用。
- `runtime.install`：可选 argv，sidecar **启动前**在包根执行（装依赖）。stdout 并进 stderr，避免污染 NDJSON。失败则不拉起 sidecar。

## 支持的能力

| 能力 | 类型 | 作用 |
|---|---|---|
| `prompt.append` | 声明式 | system 第 6 层 |
| `skill` | 声明式 | 额外 skill 根 |
| `command` | 声明式 + 代码 | markdown slash；sidecar `command.invoke` |
| `mcp` | 声明式 | 内联 `mcpServers` |
| `tool` | sidecar | `tool.execute`；模型裸名 |
| `lifecycle` | sidecar | 订事件：`initialize.subscriptions` |
| `bus` | sidecar | 扩展间总线 |

## 订事件

`initialize` result：

```json
{
  "tools": [],
  "commands": [],
  "fallback": false,
  "subscriptions": [
    { "event": "tool_call", "mode": "sync" },
    { "event": "agent_end", "mode": "async" }
  ]
}
```

- 未知 `event` 或该点不允许的 `sync`：**该条拒载**。
- 声明了 `lifecycle` 但没有任何有效订阅：**整包加载失败**。
- **sync**：停靠点 `lifecycle.invoke`（`event` + payload + `ctx`），await，应用 result。
- **async**：persist/SSE **之后** notification `lifecycle.event`；瘦 DTO；fail-open。
- 同一 event：先 sync 链，再 async（async 见最终态）。
- 链序：全局按名 → 项目按名。
- `before_agent_start`：**每个 occupy 一次**；steer 不重跑。每 turn 改 messages 用 `context`。
- sync 载荷带紧凑 `ctx`：`idle`、`model`、`aborted`。`before_agent_start` 可见 system 全文。

### Event 目录

| event | sync | async | 控制效果 |
|---|---|---|---|
| `before_agent_start` | 是 | 是 | 改 system / messages |
| `context` | 是 | 是 | 改 messages |
| `before_provider_request` | 是 | 是 | 改 request；`shortCircuit` |
| `before_provider_headers` | 是 | 是 | 改 URL/headers |
| `after_provider_response` | 否 | 是 | — |
| `provider_error` | 是（需 initialize `fallback`） | 是 | fallback text |
| `tool_call` | 是 | 是 | block / 改 args / terminate |
| `tool_result` | 是 | 是 | 改 result / terminate |
| `input` | 是 | 是 | 改写或吞掉用户输入 |
| `message_end` | 是 | 是 | 替换同 role 最终 message |
| `session_before_compact` | 是 | 是 | cancel / 定制 summary |
| `agent_start` `agent_end` `agent_settled` | 否 | 是 | — |
| `turn_start` `turn_end` | 否 | 是 | — |
| `message_start` `request_header` | 否 | 是 | — |
| `message_update` `tool_execution_update` | 否 | 否（不投） | — |
| `tool_execution_start` `tool_execution_end` | 否 | 是 | — |
| `compaction_start` `compaction_end` | 否 | 是 | — |
| `queue_changed` `steer_accepted` `run_aborted` | 否 | 是 | — |
| `mcp_server_failed` `mcp_tools_changed` | 否 | 是 | — |
| `context_usage` `patch_apply_updated` `extension_error` `extension_notice` | 否 | 否 | — |

`agent_settled`：occupy 结束且 Host 内部收尾（含 auto-compact）完成，可接受新 occupy。**不含**扩展 FIFO 已空。

### 工具批

默认并行执行一批 tool call。`tool_call` sync 按助手源序逐个跑完再执行。任一次 `terminate`（block 或 result）且该批**每个**已完成结果都 terminate 时，主循环不再请求模型。

## Sidecar 协议

NDJSON JSON-RPC 2.0。环境：`KI_EXTENSION`、`KI_SESSION_ID`、`KI_CWD`、`KI_HOME`、`KI_EXTENSION_ROOT` + 平台必需 + `runtime.env`。

超时：`initialize` 10s；sync 生命周期 2s；`tool.execute` 120s；`command.invoke` 15s。

### Host → sidecar

| method | 门闸 | 说明 |
|---|---|---|
| `initialize` | — | params：session/cwd/home/extensionRoot/capabilities；result：`{tools,commands,fallback,subscriptions}` |
| `shutdown` | — | 关闭 |
| `tool.execute` | `tool` | 进度：`tool.progress` |
| `command.invoke` | `command` | `{handled,notice,prompt}` |
| `lifecycle.invoke` | `lifecycle` + 该 event sync | 同步改流 |
| `lifecycle.event` | `lifecycle` + 该 event async | 通知 |
| `cancel` | — | `{id}` |
| `ui.action` / `ui.submit` | UI 投影 | 用户点了面板 |
| `bus.event` | `bus` | 他方 emit / 广播 |

### sidecar → Host（inbound request）

`readLoop`：有 `method`+`id` 且不是 pending response → Host 方法。**快速返回**；禁止在 sync 生命周期栈里等待整轮 run。

| method | 门闸 | 说明 |
|---|---|---|
| `session.enqueue` | — | `content`、`deliverAs`=`queue`\|`steer`\|`nextTurn`（默认 queue）、`when`=`now`\|`settled`、`idempotencyKey`、`kind`=`user`\|`custom` |
| `session.snapshot` | — | idle、running、queues、model、tools、commands |
| `session.appendEntry` | — | jsonl custom；强制本扩展名；不进 provider context |
| `session.abort` | — | 同 POST abort |
| `session.compact` | — | 同 HTTP compact |
| `session.patch` | — | model / thinkingEffort |
| `session.setActiveTools` | — | 会话级；未知名 warn，不静默清空 |
| `tools.register` | `tool` | 下一 occupy 生效 |
| `ui.setStatus` / `ui.setPanel` / `ui.clearPanel` | — | 内存投影 + SSE；不进 jsonl |
| `ui.confirm` / `ui.select` | — | WebUI 弹层；**120s** 超时 = 取消 |
| `bus.emit` | `bus` | 深拷贝 fan-out；result 为合并后 data |
| `bus.broadcast` | `bus` | fire-and-forget，不等待 |
| `bus.subscribe` / `bus.unsubscribe` | `bus` | 运行中改订阅 |

`origin` 一律 `extension:<name>`，并写进该次 occupy 的 user message（WebUI 气泡可区分）。扩展 FIFO 与用户 `queue.json` **分轨**；occupy release 后 **先用户 queue，再扩展 FIFO**。`when=settled` 在 `agent_settled` 后只写入扩展 FIFO（不直接 occupy），再走同一套 dispatch。`nextTurn` 挂到下次**用户** occupy，注入 messages，不自触发 occupy。`session.setActiveTools` 忽略未知名并发 `extension_notice` warn；全部未知名则保留上一套工具。`session.patch` 与 HTTP PATCH 同一套 ResolveSpec / thinking 校验。`session_before_compact` 可 cancel 或返回定制 summary（跳过模型摘要）。

## 扩展总线

Host 不解析 channel。协作协议（如 `workflow:mutex:v1`）由扩展自行实现。mutex：emit `{sessionId,group,busy:false}`，持锁方置 `busy=true`。

## 失败策略

默认 fail-open：RPC/超时 → `extension_error`，本 occupy 该扩展进 skip 集。`failClosed: true` 仅 `tool_call` / `before_provider_request`。

## 生命周期

1. Scan 只读 Discover。
2. `runPrompt`：`Prepare` sidecar 与 MCP 并行。
3. Steer 不重新 Prepare，不重跑 `before_agent_start`。
4. Reload / Close：杀 sidecar 进程组。忙时 Reload 排到 occupy release 之后。

## HTTP

| 方法 | 路径 | 作用 |
|---|---|---|
| GET / PATCH | `/v1/extensions?workspaceId=` | 列表 / `disabled` |
| GET | `/v1/sessions/{id}` | `availableExtensions`、`commands`、`extensionUi`、`queued`、`extQueued` |
| POST | `/v1/reload` | 关 sidecar |

`path` 只展示，不当 `href`。
