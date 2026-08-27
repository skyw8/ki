# Telegram 群组消息与离线消息

本文记录 Telegram Bot 的群组历史与离线消息处理约定，当前实现已按本文落地：

1. Bot 离线期间的 Telegram 消息重新上线后不补发。
2. 群组普通消息只进入历史，不触发回复；`@机器人` 消息读取完整群组历史并触发回复。

## 一、离线消息不补发

`telegram-bot` 启动时调用：

```json
{"drop_pending_updates": true}
```

调用顺序必须是：

```text
启动扩展
  → deleteWebhook(drop_pending_updates=true)
  → 读取本地 offset
  → getUpdates(offset)
```

这样只丢弃 Bot 离线期间尚未处理的 Telegram 更新，不影响之后新到达的消息。保留本地 offset 仍然有意义：它用于防止扩展自身重试同一更新时重复处理。

注意：这只清理 Telegram 服务端的 pending updates。已经成功进入 KI `queue.json` 的消息属于 KI 内部队列，需要在 Telegram 扩展重启时按产品策略单独清理或继续执行；本需求默认不清理已经进入 KI 的队列。

## 二、群组消息处理目标

最终语义：

```text
普通群组消息
  → 进入 context-queue.json
  → 按序写入该 chat/topic 的 session 历史
  → 不启动模型，不发送 Telegram 回复

@机器人消息
  → 写入该 chat/topic 的 session 历史
  → 读取此前已经写入的完整历史
  → 启动一次模型 prompt 并回复
```

群组仍按以下键路由：

```text
telegram:<accountId>:<chatId>:<threadId>
```

同一个群组和 topic 共用一个 session；不同用户通过 user name 和 user ID 前缀写入消息，避免模型无法区分发言者。

Telegram 侧还必须关闭 Privacy Mode，或者将 Bot 设为群组管理员，否则普通群组消息不会推送给 Bot。Privacy Mode 关闭后，扩展不能在入站层直接丢弃未 @ 消息；`mentionsBot` 只能用于判断是否触发回复，不能用于决定是否记录历史。

## 三、如何 append history

### 3.1 不能使用 `session.appendEntry`

现有 `session.appendEntry` 只追加 custom JSONL entry：

```text
session.appendEntry
  → custom entry
  → 可供 UI/审计查看
  → 不进入 session.MessagesToLeaf()
  → 不进入 provider prompt
```

所以它不能用于 Telegram 普通群组消息。

### 3.2 增加 `session.appendMessage`

扩展 Host 增加一个新的 inbound RPC：

```json
{
  "sessionId": "...",
  "message": {
    "role": "user",
    "content": [
      {"type": "text", "text": "[Telegram 用户: Alice, id=123]\n你好"}
    ],
    "origin": "extension:telegram-bot",
    "external": {
      "connector": "telegram-bot",
      "chatId": "-100123",
      "threadId": "0",
      "userId": "123",
      "messageId": "456"
    }
  },
  "idempotencyKey": "bot:8733071196:telegram-update:456"
}
```

Host 的处理逻辑：

1. 校验 `sessionId`、`message.role`、`content` 和来源扩展名。
2. 先追加到 session 目录的 `context-queue.json`，不调用 `occupy`、`runPrompt`、`loop.RunMessage`，也不写用户 `queue.json`。
3. session 空闲且没有待运行 prompt 时立即 drain；session 忙或已有待运行 prompt 时延后 drain。
4. drain 时使用 session 的正常 message entry 格式追加，等价于 `sess.AppendMessage(message)`；追加结果进入 `MessagesToLeaf()`，后续 provider prompt 可见。
5. 使用持久化 `idempotencyKey` 防止 Telegram 更新重试造成重复历史；消费队列和写 entry 之间即使崩溃，重启后也能安全重试。

不能让 sidecar 自己直接打开 session 文件。Host 必须负责每个 session 的串行写入，避免普通消息追加与 prompt 运行同时修改 active leaf 或 JSONL。

## 四、如何 start prompt

`@机器人` 消息仍然是一次真实的用户 prompt，但不应把历史消息再次拼进新消息文本。处理方式：

1. 解析并移除 Bot mention。
2. 给当前 @ 消息增加用户身份前缀和 Telegram external metadata。
3. 调用现有 `session.enqueue`，传入当前 @ 消息的 `content`、`external` 和 `idempotencyKey`。
4. prompt 入队时记录当前 context 序号；Host 在启动 prompt 前只 drain 该序号之前的 context，再通过现有 `occupy → runPrompt → loop.RunMessage` 启动模型。
5. `runPrompt` 使用 `sess.MessagesToLeaf()`；此前 context 已成为正常 message entry，因此完整历史自然进入 prompt。
6. session 忙时，@ 消息进入现有扩展 FIFO，当前运行结束后再启动；不创建第二个并发模型运行。

伪代码：

```text
if group && !mentionsBot(message):
    session.appendMessage(contextMessage) // Host 持久化并按边界 drain
    return

if group && mentionsBot(message):
    text = stripBotMention(message)
    session.enqueue(
        content=telegramUserMessage(text),
        external=telegramMetadata(message),
        idempotencyKey=updateKey,
    )
    return
```

## 五、运行中消息的顺序保证

仅仅在 sidecar 中先调用 `appendMessage`、再调用 `session.enqueue` 不够可靠：

```text
普通消息 A → @消息 B → 普通消息 C
```

如果 B 正在等待运行，C 不能被直接追加到 session 并被 B 的 prompt 读到，否则 C 会错误地出现在 B 之前的上下文中。

当前 Host 侧使用 session 目录中的持久化 context queue 和 prompt 边界序号，至少支持两种 item：

```text
context  → appendMessage，不启动模型
prompt   → 先提交此前的 context，再进入现有 session.enqueue
```

Telegram 扩展按 Telegram `update_id` 顺序写入该 FIFO。Dispatcher 保证：

1. FIFO 中的 context item 先按顺序成为正常 message entry。
2. 遇到 prompt item 时，先完成它之前的 context item，再把该 prompt 交给现有运行/排队逻辑。
3. prompt 之后到达的 context item 只能影响下一次 prompt。
4. FIFO 必须持久化，重启后不能丢失已经收到但尚未写入 session 的普通消息。
5. 每个 item 使用 Telegram update ID 做幂等，避免轮询重试重复追加。

Host 通过同一个 session input gate 串行化 `appendMessage`、`enqueue` 和 dispatch；sidecar
不直接打开 session 文件，也不自行保证两个 RPC 的竞态顺序。

## 验收标准

- 启动时 `drop_pending_updates=true`，Bot 离线期间的 Telegram 消息不再补发。
- Privacy Mode 关闭后，普通群组消息能被接收，但不会产生模型运行或 Telegram 回复。
- 普通群组消息以正常 `message` entry 出现在 session 历史中。
- `@机器人` 时，prompt 包含此前普通消息、当前 @ 消息和正确的用户身份标识。
- `@机器人` 之后才到达的普通消息不会混入当前 prompt，只能进入下一次 prompt。
- session 忙、扩展重启、Telegram update 重试时，消息顺序和幂等性保持正确。
