# 扩展（Extensions）

包是带 `extension.json` 的目录。声明式贡献并入 Snapshot；需要跑代码时只走 **NDJSON JSON-RPC 2.0 sidecar**（语言无关，不编进 ki）。实现包：`internal/extension`。

**不做**旧 `hook` / `intercept` / `intercept[]` 协议。代码能力对外只订事件（`lifecycle`）+ 可选 inbound Host 方法。

## 发现与开关

| 位置 | Scope |
|---|---|
| `{KI_HOME}/extensions/<name>/` | 全局 |

- `name` 是主键。`name` 须匹配 `^[a-z0-9][a-z0-9-]{0,62}$`，禁止 `ki.` 前缀。
- 启用开关是进程级 `{KI_HOME}/toggles.json` 的 `extensions.disabled`（缺省空 = 全开）。
- 禁用的包仍出现在列表（`enabled: false`），但不贡献、不拉起 sidecar。
- 目录列表和 prompt/lifecycle 链均按全局包名排序。

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
  "capabilities": ["prompt.append", "skill", "command", "tool", "lifecycle", "bus", "provider", "channel", "settings"],
  "failClosed": false,
  "prompt": { "append": ["prompt/APPEND.md"] },
  "skills": ["skills"],
  "commands": ["commands"],
  "providers": [{
    "id": "example-provider",
    "name": "Example Provider",
    "api": "example-responses",
    "baseUrl": "https://example.invalid/api",
    "auth": { "type": "oauth", "subscription": true },
    "models": [{ "id": "example-model", "contextWindow": 128000, "maxTokens": 16384, "input": ["text"] }]
  }],
  "runtime": { "kind": "rpc", "command": "bin/extension", "args": [], "install": [], "env": {} }
}
```

- `capabilities`：门闸。未声明的能力：对应字段忽略；`initialize` 多报的 tools/commands 丢弃并 `extension_error`。
- `failClosed`：缺省 `false`。仅 **sync** 生命周期入口：`tool_call` 失败 → 合成 block；`before_provider_request` 失败 → canned stop。
- `runtime.kind`：`none`（缺省）| `rpc`。`rpc` 须声明 `tool` / `lifecycle` / `command` / `bus` / `provider` / `channel` / `settings` 之一。
- `runtime.command`：无路径分隔符（`node` / `npx`）走 **PATH**；带 `/` 的相对路径相对包根（`bin/extension`）；绝对路径原样用。
- `runtime.install`：可选 argv，sidecar **启动前**在包根执行（装依赖）。stdout 并进 stderr，避免污染 NDJSON。失败则不拉起 sidecar。

## 支持的能力

| 能力 | 类型 | 作用 |
|---|---|---|
| `prompt.append` | 声明式 | system 第 6 层 |
| `skill` | 声明式 | 额外 skill 根 |
| `command` | 声明式 + 代码 | markdown slash；sidecar `command.invoke` |
| `tool` | sidecar | `tool.execute`；模型裸名 |
| `lifecycle` | sidecar | 订事件：`initialize.subscriptions` |
| `bus` | sidecar | 扩展间总线 |
| `provider` | 进程级 sidecar | 注册模型/认证元数据并接管 provider stream |
| `channel` | 进程级 sidecar | 接入外部消息渠道并调用 Host session 能力 |
| `settings` | 进程级 sidecar | 声明全局配置 schema，由 Host 脱敏、校验和通知变更 |

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
- **async**：persist/SSE **之后** notification `lifecycle.event`；瘦 DTO；fail-open。同一 run 的通知保持 loop 产生顺序，尤其 `message_end` 必须先于该 run 的 `agent_settled`。
- 同一 event：先 sync 链，再 async（async 见最终态）。
- 链序：全局按名。
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
| `message_start` `request_header` `message_update` | 否 | 是 | — |
| `tool_execution_update` | 否 | 否（不投） | — |
| `tool_execution_start` `tool_execution_end` | 否 | 是 | — |
| `compaction_start` `compaction_end` | 否 | 是 | — |
| `queue_changed` `steer_accepted` `run_aborted` | 否 | 是 | — |
| `context_usage` `patch_apply_updated` `extension_error` `extension_notice` | 否 | 否 | — |

`agent_settled`：occupy 结束且 Host 内部收尾（含 auto-compact）完成，可接受新 occupy。**不含**扩展 FIFO 已空。

### 工具批

默认并行执行一批 tool call。`tool_call` sync 按助手源序逐个跑完再执行。任一次 `terminate`（block 或 result）且该批**每个**已完成结果都 terminate 时，主循环不再请求模型。

## Sidecar 协议

NDJSON JSON-RPC 2.0。环境：`KI_EXTENSION`、`KI_HOME`、`KI_EXTENSION_ROOT` + 平台必需 + `runtime.env`。全局 sidecar 不固定 `KI_SESSION_ID`/`KI_CWD`；session 相关 RPC 显式携带 `sessionId`。

超时：`initialize` 10s；sync 生命周期 2s；`tool.execute` 120s；`command.invoke` 15s；provider stream start 10s。

### Host → sidecar

| method | 门闸 | 说明 |
|---|---|---|
| `initialize` | — | params：home/extensionRoot/capabilities/scope/providers；全局扩展的 `sessionId`/`cwd` 为空；result：`{tools,commands,fallback,subscriptions}` |
| `shutdown` | — | 关闭 |
| `tool.execute` | `tool` | `{sessionId,...}`；进度：`tool.progress` |
| `command.invoke` | `command` | `{sessionId,name,args}`；result：`{handled,notice,prompt}` |
| `lifecycle.invoke` | `lifecycle` + 该 event sync | `{sessionId,...}`；同步改流 |
| `lifecycle.event` | `lifecycle` + 该 event async | `{sessionId,...}` 通知 |
| `cancel` | — | `{id}` |
| `session.open` / `session.close` | — | `{sessionId,cwd}` / `{sessionId}`；通知全局 sidecar 建立或释放该 session 的业务视图 |
| `ui.action` / `ui.submit` | UI 投影 | 用户点了面板 |
| `bus.event` | `bus` | 他方 emit / 广播 |
| `provider.stream.start` | `provider` | `{requestId,request}`；一次传入完整 model、credential 和 loop request，`request` 使用 lower camelCase 字段名，返回 `{accepted:true}` |
| `provider.stream.cancel` | `provider` | `{requestId}`；取消一个 provider stream |

`config.updated` 是 Host 发给全局 sidecar 的配置变更通知，参数包含脱敏后的
`config`；sidecar 应重新读取自己的私有配置文件。

异步消息生命周期事件的瘦 payload 只包含路由和展示所需字段。`message_start`、
`message_update`、`message_end` 会携带 `role`、`text`；最终消息还可能携带
`stopReason`、`errorMessage` 和 `isError`。当 `stopReason=error` 时，扩展应丢弃
已经收到的 partial 文本，并向用户展示错误信息，而不是把 partial 当成最终答案。

Provider auth RPC（同样只发给进程级 provider sidecar）：

| method | 说明 |
|---|---|
| `provider.auth.start` | `{requestId,provider,mode}`，`mode` 为 `browser` 或 `device_code`；立即返回 accepted |
| `provider.auth.input` | `{requestId,provider,value}`；提交 redirect URL 或手工 authorization code |
| `provider.auth.cancel` | `{requestId,provider}`；取消未完成的登录 |
| `provider.auth.refresh` | `{provider,credential}`；sidecar 决定是否刷新，返回新的 opaque credential |

sidecar 通过 `provider.auth.event` notification 报告 `auth_url`、`device_code`、`completed`、`error`。`completed` 的 credential 只在 sidecar 与 server auth broker 之间传递，server 对 WebUI/CLI 只返回状态、URL 和设备码。

provider capability 使用进程级 sidecar，不随 session 各拉起一个进程。provider 只能从 `{KI_HOME}/extensions` 声明；`providers` 是扩展清单中的离线目录，provider sidecar 只负责对应 provider 的认证/网络/响应解析；宿主只保留模型目录、凭据状态、取消、背压和 loop 适配。一次 stream 的结果通过 sidecar → Host 的 `provider.stream.event` notification 回传：

```json
{"jsonrpc":"2.0","method":"provider.stream.event","params":{"requestId":"stream-1","type":"text_delta","contentIndex":0,"delta":"hello"}}
```

事件类型首版为 `start`、`text_start`/`text_delta`/`text_end`、`thinking_start`/`thinking_delta`/`thinking_end`、`toolcall_start`/`toolcall_delta`/`toolcall_end`、`custom_tool_call_input_delta`、`done`、`error`。`done` 可携带完整最终 `message`；普通增量不重复携带完整 `partial`，Host adapter 会在内存中重建 `loop.AssistantDelta`。

provider sidecar 的生命周期、凭据和流都是全局进程级资源；session 只通过 `requestId` 复用同一个 sidecar。Reload 时保留仍注册的 sidecar，移除或禁用的 provider 会关闭对应进程。

### sidecar → Host（inbound request）

`readLoop`：有 `method`+`id` 且不是 pending response → Host 方法。**快速返回**；禁止在 sync 生命周期栈里等待整轮 run。

| method | 门闸 | 说明 |
|---|---|---|
| `session.create` | — | 全局创建 session；可传 `workspaceId`、`cwd`、model 和 metadata |
| `session.list` / `session.get` | — | 查询 session，可按 metadata 过滤；不绑定当前 session |
| `session.new` | — | 当前 session 创建同配置的新 session，可选新 `cwd` |
| `session.reload` | — | 重载当前 session 的资源和扩展视图 |
| `session.enqueue` | — | `{sessionId,...}`；`content`、`deliverAs`=`queue`\|`steer`\|`nextTurn`（默认 queue）、`when`=`now`\|`settled`、`idempotencyKey`、`kind`=`user`\|`custom` |
| `session.snapshot` | — | `{sessionId}`；idle、running、queues、provider/model、tools、commands |
| `session.appendEntry` | — | `{sessionId,...}`；jsonl custom；强制本扩展名；不进 provider context |
| `session.abort` | — | 同 POST abort |
| `session.compact` | — | 同 HTTP compact |
| `session.patch` | — | model / thinkingEffort |
| `session.setActiveTools` | — | 会话级；未知名 warn，不静默清空 |
| `tools.register` | `tool` | 下一 occupy 生效 |
| `ui.setStatus` / `ui.setPanel` / `ui.clearPanel` | — | 当前 session 的内存投影 + SSE；不进 jsonl。面板是通用壳，不解析业务 |
| `ui.setGlobalStatus` / `ui.setGlobalPanel` / `ui.clearGlobalPanel` | — | server 级内存投影；不进 jsonl；通过 `/v1/extensions` 返回，适合首页可见的全局状态；global panel 只读，配置走 config API |
| `ui.confirm` / `ui.select` | — | WebUI 弹层；**120s** 超时 = 取消 |
| `bus.emit` | `bus` | 深拷贝 fan-out；result 为合并后 data |
| `bus.broadcast` | `bus` | fire-and-forget，不等待 |
| `bus.subscribe` / `bus.unsubscribe` | `bus` | 运行中改订阅 |

除 `session.create`、`session.list`、`session.get` 和 `ui.setGlobal*` / `ui.clearGlobalPanel` 外，上表 inbound 方法都必须带 `sessionId`，bus 订阅也按 session 维护。`session.open` 是 Host→sidecar 的 session 生命周期通知。`ui.setPanel` 和 `ui.setGlobalPanel` 由 WebUI 按通用壳渲染，Host 不解析扩展语义。壳的面、投影、字段表和 `ui.action` / `ui.submit` 见 [webui.md 扩展 UI 壳](webui.md#扩展-ui-壳)。

`origin` 一律 `extension:<name>`，并写进该次 occupy 的 user message（WebUI 气泡可区分）。扩展 FIFO 与用户 `queue.json` **分轨**；occupy release 后 **先用户 queue，再扩展 FIFO**。`when=settled` 在 `agent_settled` 后只写入扩展 FIFO（不直接 occupy），再走同一套 dispatch。`nextTurn` 挂到下次**用户** occupy，注入 messages，不自触发 occupy。`session.setActiveTools` 忽略未知名并发 `extension_notice` warn；全部未知名则保留上一套工具。`session.patch` 与 HTTP PATCH 同一套 ResolveSpec / thinking 校验。`session_before_compact` 可 cancel 或返回定制 summary（跳过模型摘要）。

## 扩展总线

Host 不解析 channel。协作协议（如 `workflow:mutex:v1`）由扩展自行实现。mutex：emit `{sessionId,group,busy:false}`，持锁方置 `busy=true`。

## 失败策略

默认 fail-open：RPC/超时 → `extension_error`，本 occupy 该扩展进 skip 集。`failClosed: true` 仅 `tool_call` / `before_provider_request`。

## 生命周期

1. Scan 只读 Discover；sidecar 是进程级资源，每个扩展最多启动一个。
2. **server 完成监听后**，所有启用且声明可执行 runtime 的扩展统一启动，不按能力类别或 session 懒加载；失败状态由 catalog 暴露并自动重试。
3. `GET /v1/sessions/{id}` 带 `runtime.ready`；未就绪也可先出 transcript。`runtime_ready` SSE（sideband，不进 jsonl）。失败也算 ready。
4. `Prepare` 只建立当前 session 的扩展视图，复用已运行的 server sidecar，并发送 `session.open`。
5. Steer 不重新 Prepare，不重跑 `before_agent_start`。
6. Reload / Close：Reload 重新扫描 manifest、配置并重启有变化的 sidecar；server Close 才杀全局 sidecar 进程组。忙时 Reload 排到 occupy release 之后。

## HTTP

| 方法 | 路径 | 作用 |
|---|---|---|
| GET / PATCH | `/v1/extensions` | 全局列表 / `disabled`，包含 runtime 状态和全局 `ui` 投影 |
| GET / PATCH | `/v1/extensions/{name}/config` | 读取脱敏配置 / 校验并保存扩展配置 |
| GET | `/v1/sessions/{id}` | `availableExtensions`、`commands`、`extensionUi`、`queued`、`extQueued`、`runtime.ready` |
| POST | `/v1/reload` | 重新扫描并协调 sidecar |

`path` 只展示，不当 `href`。

扩展 catalog 展示启用配置、manifest 错误、server 级 runtime 状态和 global UI 投影；查询不会启动 sidecar。manifest 校验失败不会拉起 runtime；sidecar 启动失败保持启用并自动重试。配置接口只返回 schema 和脱敏值，敏感字段写入时保留、读取时显示 `<configured>`。全局 extension chip 和 goal 等 session status chip 共用同一个扩展 Modal；左侧导航切换扩展，Extensions 设置中的 Configure 也只负责打开并定位到同一个页面，不在 session Info 或设置页内嵌第二份编辑器。
