# Telegram Bot 接入方案

## 调研结论

- Telegram 官方 Bot API 是 HTTPS 接口，不依赖 Go 专用 SDK。Go 侧使用标准库 `net/http` 复用一个 `http.Client`，请求地址为 `https://api.telegram.org/bot<token>/METHOD_NAME`，普通请求使用 JSON，文件使用 multipart。
- 首版采用 `getUpdates` 长轮询：实现简单，不要求公网 HTTPS；用 `offset=update_id+1` 持久化确认进度。`getUpdates` 与 webhook 互斥，后续有公网入口时再支持 `setWebhook`。
- 普通 Bot 没有通用“标记已读”接口。`getUpdates` 的 offset 只能确认 Bot 已处理更新，不等同于用户客户端的已读状态；`readBusinessMessage` 仅适用于 Business Bot。收到消息后可以调用 `setMessageReaction` 打一个 `👀`/`⏳` reaction，失败不阻断消息处理。
- Telegram 支持流式输出，但 `sendMessageDraft` 是私聊专用、最长约 30 秒的临时预览，客户端会在收到同一 chat/topic 的普通消息或 TTL 到期时移除；最终必须调用 `sendMessage` 固化。群组使用“先发送占位消息，再周期性 `editMessageText`”，或只发送最终结果。
- Managed Bot 支持由 Telegram 侧限制访问用户：所有者始终可访问，也可以增加最多 10 个数字 `user_id`。该策略不提供群组白名单或命令级权限；本扩展只配置 Managed Bot 的 `botId` 和 `token`，不重复维护安全策略。
- Managed Bot 的创建需要先准备一个开启 Bot Management Mode 的 Manager Bot，再通过 `https://t.me/newbot/<manager_username>/<suggested_username>?name=<suggested_name>` 创建。Manager Bot 通过 `getManagedBotToken` 获取子 Bot token，扩展使用子 Bot 的 `botId` 和 token；Manager Bot token 不填入扩展。
- 群组继续保持 Telegram Privacy Mode，并在扩展内再次强制判断：只有明确 @ 当前 Bot，才触发回复。优先使用 `entities` 中的 `mention`、`text_mention` 或 `bot_command`；实体缺失时仅按完整 username 做边界匹配，不使用简单字符串包含判断。
- Telegram 的 `chat.id`、可选的 `message_thread_id`、`from.id`、用户名/显示名都需要保留。session 按 chat/thread 映射；群组内每条消息额外带紧凑的发送者身份，避免多人共享上下文时无法区分发言人。

## 现状可行性结论

当前扩展能力可以完成“已有 session 的入站注入”和部分生命周期通知，但不能在不改动扩展系统的情况下稳定实现完整 Telegram 扩展：

- 可以利用 `session.enqueue` 注入消息，利用 `agent_settled`、`run_aborted`、`queue_changed` 感知运行状态，利用 `tool_execution_start/end` 做简短工具状态提示。
- 不能可靠地在 server 启动时拉起独立 Telegram worker；普通 sidecar 依赖 session 准备后才启动。
- 没有标准的 session 创建/发现 RPC，也没有面向 channel 的 session 输出订阅；当前 `message_update` 和工具进度不属于可直接订阅的生命周期流。
- 通过读取 `server.json` 后自行调用现有 HTTP/SSE 接口，可以做一个绕过式实现，但需要扩展自行处理认证、session 创建、SSE 重连、事件过滤和并发，且不属于稳定的扩展契约。

结论：当前生命周期需要调整。应先完成阶段一的统一启动机制，再实现阶段二。若只要求验证 Telegram 收发链路，可以用现有 HTTP/SSE 做临时原型；要支持重启恢复、多个 chat、群组 topic、流式输出和可靠投递，则必须使用新的统一生命周期。

## 统一扩展生命周期

目标是不按普通 extension、provider、channel 等类别区分启动时机。所有声明了可执行 runtime 的启用扩展，均在 server 完成监听后统一加载；能力差异只用于权限和协议路由，不影响生命周期。

```text
server 监听成功
  → 扫描并校验启用的 runtime 扩展
  → 每个扩展启动一个 server 级 sidecar
  → initialize(scope=global)
  → sidecar ready / failed / restarting
  → 有 session 时发送 session.open / session.close
  → server shutdown 时统一发送 shutdown 并关闭 sidecar
```

- `tool`、`lifecycle`、`command`、`bus`、`provider`、`channel` 等能力统一由同一个 server 级 sidecar 生命周期管理；不再因能力不同而懒加载或使用不同的启动管理器。
- `runtime.kind=none` 的 prompt、skill、静态 command 只有声明式内容，不启动 sidecar；这不是运行时类别，而是没有可执行进程。
- server 启动后异步加载扩展，单个扩展启动失败不能阻塞 HTTP 服务；WebUI 和日志显示每个扩展的状态。
- `initialize` 不绑定 session 或 cwd；session 相关能力通过显式 `sessionId` 调用，`session.open/close` 只负责建立和释放 session 视图。
- provider 的模型注册、provider stream 和普通 extension RPC 复用同一个扩展进程；不再单独按 provider 首次请求懒加载。
- reload 重新扫描 manifest 和配置，只重启新增、删除、禁用或内容变化的 sidecar；未变化的 sidecar 保持运行。

## Telegram session 与 workspace

### Session 元信息

session 的 `cwd` 一旦写入 header 就不应原地修改；`/cwd` 应创建新 session，再切换 Telegram 映射。session 的 `config.json` 增加通用 `metadata`，例如：

```json
{
  "metadata": {
    "source": "telegram",
    "connector": "telegram-bot",
    "accountId": "bot:123456",
    "externalKey": "telegram:bot:123456:-100123:42",
    "chatId": "-100123",
    "threadId": "42",
    "chatType": "supergroup"
  }
}
```

- `source` / `connector` 用于识别来源；`accountId` 防止多个 Bot 的 chat ID 冲突。
- `externalKey` 是稳定路由键：`telegram:<accountId>:<chatId>:<threadId>`，没有 topic 时 `threadId=0`。
- ID 全部按字符串保存，避免 Telegram 的大整数在 JSON 或 JavaScript 中丢精度。
- 只保存路由所需元信息，不保存 Bot token、完整消息正文或附件内容。扩展自己的 `externalKey → sessionId` 映射和 update offset 单独持久化；重启后可通过元信息扫描恢复映射。

### 默认 workspace

- `{KI_HOME}/workspace/telegram` 只作为 Telegram 的目录命名空间，默认 `KI_HOME` 为 `~/.ki`，不把它本身作为所有 chat 共用的 workspace。
- 每个 Bot 的每个 chat/topic 使用独立目录，例如：

  ```text
  {KI_HOME}/workspace/telegram/bot-123456/chat--100123/topic-42
  {KI_HOME}/workspace/telegram/bot-123456/chat-987654321/topic-0
  ```

- 目录名只使用稳定 ID，不使用 chat title/username；通过 `filepath.Join` 组合路径。`chatId`、`threadId` 和 Bot ID 均按字符串处理，避免特殊字符和整数精度问题。
- 首次使用时由 Host 创建并登记实际的 chat/topic 目录到 `{KI_HOME}/workspaces.json`，title 可为 `Telegram · <chatId> · <threadId>`，并将 `temp` 设为 false。
- Telegram 新建 session 时显式传入对应 `workspaceId`，或传入已创建的 chat/topic 目录让 Host 登记为 workspace；禁止省略工作目录触发 `EnsureTemp`。
- 默认按 `chatId + threadId` 隔离 workspace，避免不同群组或不同 topic 共享文件；如明确需要共享文件，再通过 `/cwd` 选择同一个外部 workspace。
- `/cwd <path>` 成功后，为目标目录登记/复用 workspace，并在同一个 `externalKey` 下创建新 session；旧 session 保留用于历史查看。

### 命令职责

Telegram sidecar 应在入站层识别 slash command，调用核心 session 能力，不把控制命令作为模型 prompt。核心系统提供统一的 command service，使 WebUI、HTTP 和 Telegram 行为一致：

| 命令 | 核心行为 | Telegram 映射行为 |
|---|---|---|
| `/new` | 创建同 workspace、同模型、同 metadata 的空 session，返回新 ID | 将当前 `externalKey` 指向新 ID，旧 session 保留 |
| `/cwd <path>` | 校验/登记目录并创建新 session；不修改旧 session 的 cwd | 更新映射并回复新工作目录；可用性由 Telegram Managed Bot 访问策略决定 |
| `/compact` | 调用已有 session compact；忙时按核心规则返回冲突或排队 | 回复“已开始/完成/失败”，不送入模型 |
| `/reload` | 只重载当前 session 的 skills 和扩展视图 | 调用 session 级 reload，不触发全局 Bot 重启 |

阶段一应新增或统一以下 Host 能力：`session.create`（支持 workspace 和 metadata）、`session.new`、`session.reload`、`session.list`/`session.get`（可按 metadata 过滤）。现有 `session.compact`、`session.abort` 可以直接复用。`/new` 和 `/cwd` 的核心服务应返回 `{sessionId, cwd, workspaceId, metadata}`，方便 channel 更新映射。

`/new`、`/cwd` 在普通 HTTP prompt 路径中由核心 parser 处理；Telegram 因为使用扩展入站 RPC，应在 sidecar 中识别后调用同一组 service/RPC。这样不会依赖 `session.enqueue` 把 slash 文本误当成普通模型输入。

## WebUI 配置

Managed Bot 的访问策略在 Telegram 侧配置，不在扩展中重复配置。对话页右上角显示全局扩展 chip，点击后进入与 goal 等 panel 共用的统一 Extension Modal；全局设置中的 Configure 复用同一个页面。

扩展只配置一个 Managed Bot。WebUI 使用专用表单，不需要编辑 JSON：

- **Managed Bot ID**：填写 Managed Bot 的数字 ID。
- **Bot Token**：填写 Managed Bot token；已配置时读取不会回显，留空表示保持原值。
- **回复模型**：从当前模型列表弹窗中选择；留空表示跟随全局默认模型。

- `botId` 按字符串保存；扩展通过 `getMe` 校验 token 与 ID 是否匹配，并将 `bot:<botId>` 作为稳定的 `accountId`。
- `token` 只允许写入，Manager Bot 的 token 不能填入这里。
- 不再配置 `accounts`、私聊/群组白名单、群组管理员策略或命令权限。`/new`、`/cwd`、`/compact`、`/reload` 的可用性由 Telegram Managed Bot 的访问限制统一保护。
- 默认 workspace 根目录显示为 `{KI_HOME}/workspace/telegram`，实际 chat/topic 子目录由扩展按 ID 自动创建。
- 保存配置时由 Host 校验 schema、以 0600 权限持久化到扩展配置目录，并通过 `config.updated` 重启 Bot worker；已有 chat→session 映射不丢失。
- Bot 是否允许加入群组仍由 BotFather 的 `/setjoingroups` 控制；扩展不维护群组 chat ID 白名单，但群组消息始终要求明确 @ 当前 Bot。

阶段一已提供通用接口：`GET/PATCH /v1/extensions/{name}/config`；`POST /v1/extensions/{name}/config/test` 作为后续可选增强。接口只返回脱敏配置和 schema，不返回 token。Telegram 配置页在此通用接口之上提供专用表单，其他扩展仍可使用通用 JSON 编辑器；配置更新通过通用 `config.updated` 通知 sidecar。

## 阶段一：移除 MCP 并扩展系统能力

目标是增加通用的 channel/connector 和 settings/config 能力，不在 Host 中写 Telegram 专用逻辑；同时完全移除 MCP。后续不再支持或扩展 MCP，Stateless MCP、server 启动时加载等方案不再实施。

本阶段必须同时完成 MCP 移除：删除 MCP 配置、连接管理、工具发现、工具注入、相关事件、设置接口和文档契约。阶段一完成后，项目不再支持 MCP，扩展和 session 生命周期也不再依赖 MCP。

1. 增加进程级后台扩展能力。

   - 新增 `channel`（或 `connector`）能力。
   - 新增通用 `settings` / `config` 能力：扩展声明配置 schema，WebUI 负责渲染和保存，Host 负责校验、脱敏和通知变更。
   - sidecar 在 server 监听成功后统一启动，支持停止、健康状态和崩溃后的重新拉起，不依赖用户先打开某个 session。
   - 保留现有 `session.open` / `session.close`，用于维护具体 session 视图。

2. 增加 session 管理 RPC。

   - `session.list`：列出可用 session。
   - `session.create`：按 workspace、model、metadata 创建 session；无 workspace 时由 channel 明确选择默认 workspace，不能隐式创建临时 workspace。
   - `session.new` / `session.reload`：支持 `/new`、`/cwd`、`/reload` 的统一核心语义。
   - 扩展 `session.enqueue`：支持 `idempotencyKey` 和外部来源元数据。
   - 增加 `session.events.subscribe` 或等价的 session 事件订阅能力。
   - 增加附件上传能力，避免 channel sidecar 依赖内部 HTTP API。

3. 增加外部消息身份和关联信息。

   入站消息携带类似下面的元数据：

   ```json
   {
     "source": "telegram",
     "chatId": "-100123",
     "threadId": "42",
     "userId": "987",
     "messageId": "301"
   }
   ```

   这些字段写入消息/事件用于路由和回复，但默认不直接发送给模型；模型需要知道身份时，由 Host 注入简短的 author 前缀。

4. 暴露安全的输出流。

   - 允许扩展订阅 `message_start`、文本型 `message_update`、`message_end`。
   - `message_update` 只提供文本增量，不提供 thinking、完整历史或敏感附件。
   - `tool_execution_start/end` 只提供 `toolName` 和可选的展示标题，不默认暴露 args/result。
   - 每个事件携带 `sessionId`、`runId` 和外部关联信息，便于回复到正确的 chat。

5. 保留现有能力的职责边界。

   - `session.enqueue`：入站消息。
   - `agent_settled`、`run_aborted`、`queue_changed`：运行状态和队列状态。
   - `tool_execution_start/end`：工具状态提示。
   - `ui.setStatus` / `ui.setPanel`：WebUI 配置和状态展示。
   - `command`：由核心统一提供 `/new`、`/cwd`、`/compact`、`/reload`；扩展可增加自己的命令。
   - 不使用 `provider` 能力；它用于接入 LLM provider，不是外部聊天渠道。

## 阶段二：实现 `telegram-bot` 扩展

### 基础流程

```text
Telegram Update
  → 去重并确认 update_id
  → reaction（可选）
  → 私聊直接处理 / 群组检查 @Bot
  → 解析 chat/thread/user 身份
  → chat/thread 查找或创建 session
  → session.enqueue
  → 接收 ki 输出事件
  → Telegram 草稿/占位消息更新
  → 发送最终回复
```

### 入站

- sidecar 使用 Go `net/http` 实现 `getUpdates` 长轮询，持久化 offset，并对 Telegram 429/网络错误做退避重试。
- 收到 `message` 后先尝试 `setMessageReaction`，默认使用 `👀` 表示已接收；普通 Bot 不执行真正的“已读”操作。
- 私聊直接接受文本、图片和文件；媒体先通过 Telegram `getFile` 下载，再转换成 ki 的 attachment/content。可访问用户由 Managed Bot 的 Telegram 原生策略决定。
- 群组仅接受明确 @ 当前 Bot 的消息；优先按 Telegram entities 判断，entities 缺失时按完整 username 做安全兜底；去除 @Bot 文本后再送入模型。即使 Bot 是管理员或关闭了 Privacy Mode，也继续执行该过滤；扩展不额外维护群组白名单。
- 群组消息的模型输入包含简短身份信息，例如：

  ```text
  [Telegram 用户: Alice, id=987]
  请检查这个错误
  ```

  身份以 `from.id` 为准，username 和显示名只作展示。

### chat 与 session 映射

- 默认映射键：`telegram:<accountId>:<chatId>:<threadId>`；没有 topic 时 `threadId=0`。
- 私聊通常是一 chat 一 session。
- 群组所有用户共享一个 chat session，但每条 user message 都带 author 身份。
- workspace 映射键与 session 映射键一致，并额外包含 Bot `accountId`；`externalKey` 同时决定 session 和默认 workspace，避免不同 chat 复用工作目录。
- 映射、最近发送消息、Telegram update offset 持久化在扩展自己的状态文件中；不把 Bot token 写入 session jsonl。
- 首版提供 `/new`、`/cwd`、`/compact`、`/reload`；不在扩展中增加命令级权限配置，统一依赖 Telegram Managed Bot 访问策略。

### 输出与内容优化

- 只发送 assistant 文本；不发送 thinking、原始 tool call、tool args 和完整 tool result。
- `tool_execution_start` 时显示简短状态，例如 `🔧 读取文件`、`🔧 执行命令`，标题由工具名映射得到；结束后清除或合并为一行状态。
- 私聊优先使用同一个非零 `draft_id` 调用 `sendMessageDraft` 流式更新，最终 `message_end` 使用 `sendMessage` 固化；`agent_settled` 只做延迟兜底，不能清空尚未固化的文本。群组发送一个占位消息并用 `editMessageText` 更新，编辑失败则降级为最终消息。`stopReason=toolUse` 只结束当前 assistant turn，不能结束整个 Telegram 回复；收到 `stopReason=error` 时清空 partial 文本并显示错误原因。
- Host 和 sidecar 都必须按同一 run 的产生顺序投递生命周期事件，避免 `agent_settled` 越过 `message_end` 后只留下会自动消失的 live draft。
- 如果阶段一暂不提供文本增量，则先发送 `sendChatAction(typing)`，最后发送完整结果。
- 首版发送纯文本，避免 HTML/MarkdownV2 转义差异；按 4096 字符限制切分，工具状态和正文分开处理，后续再增加格式化输出。
- `message_end` 作为最终发送兜底，发送任务放入 sidecar 队列，不在同步生命周期 hook 中等待网络请求。

### 验收范围

- 私聊：收消息、reaction、入队、最终回复、取消、session 持久映射。
- 群组：只有 @Bot 才回复，多用户身份可区分，支持 topic/thread 映射。
- 输出：私聊可流式，群组可占位消息编辑或最终回复；不泄露 thinking、tool args 和完整 tool result。
- 稳定性：update 去重、重启恢复 offset 和映射、session 忙时排队、Telegram 网络重试、sidecar 停止时优雅退出。

官方参考：

- [Telegram Bot API](https://core.telegram.org/bots/api)
- [Telegram Privacy Mode](https://core.telegram.org/bots/features#privacy-mode)
- [Telegram Bot API：getUpdates / setWebhook](https://core.telegram.org/bots/api#getting-updates)
- [Telegram Bot API：sendMessageDraft](https://core.telegram.org/bots/api#sendmessagedraft)
- [Telegram Bot API：setMessageReaction](https://core.telegram.org/bots/api#setmessagereaction)
- [Go `net/http`](https://pkg.go.dev/net/http)
