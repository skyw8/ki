# Telegram Managed Bot 扩展

这个扩展通过 Telegram Bot API 长轮询接入 KI，支持私聊、群组、群组 topic、图片和文件。每个 `chatId + threadId` 对应独立的 session 和 workspace。

## 1. 创建并配置 Managed Bot

这里有两个 Bot，职责不同：

- **Manager Bot**：负责创建和管理其他 Bot，只保存它自己的 token，不接入 KI。
- **Managed Bot**：真正接收 Telegram 消息并接入 KI。它由创建者拥有，token 要填入本扩展。

### 1.1 创建 Manager Bot

如果还没有 Manager Bot：

1. 在 [@BotFather](https://t.me/BotFather) 发送 `/newbot`。
2. 按提示填写 Bot 的显示名称和用户名。用户名必须以 `bot` 结尾，例如 `KIManagerBot`。
3. 保存 BotFather 返回的 **Manager Bot token**。
4. 打开 [BotFather Mini App](https://t.me/BotFather)，进入这个 Bot 的设置，开启 **Bot Management Mode**。

也可以选择一个已有 Bot 开启 Bot Management Mode，不必重新创建。

### 1.2 通过 Manager Bot 创建 Managed Bot

由希望拥有 Managed Bot 的 Telegram 用户，在 Telegram 客户端打开下面的链接：

```text
https://t.me/newbot/<ManagerBot用户名>/<建议的ManagedBot用户名>?name=<建议的显示名称>
```

例如 Manager Bot 是 `@KIManagerBot`：

```text
https://t.me/newbot/KIManagerBot/KIAgentBot?name=KI%20Agent
```

操作过程如下：

1. 将链接发给自己，或直接在 Telegram 中打开链接。
2. Telegram 会打开创建 Bot 的确认页面；其中的名称和用户名只是预填值，可以修改。
3. 确认创建。当前登录的 Telegram 用户会成为 Managed Bot 的所有者。
4. 创建完成后，Manager Bot 会收到一条 `managed_bot` 更新，其中包含新 Bot 的信息。

Managed Bot 不需要再对 BotFather 执行 `/newbot`；`/newbot` 只用于创建普通 Bot（包括 Manager Bot）。官方把这个链接称为 Managed Bot creation deep link。[官方说明](https://core.telegram.org/bots/features#managed-bots)

### 1.3 获取 Managed Bot token 和 ID

Manager Bot 收到创建更新后，必须使用 **Manager Bot token** 调用 Bot API 的 `getManagedBotToken`。如果 Manager Bot 还没有自己的管理程序，可以先用长轮询取出这条更新：

```bash
curl -sS -G \
  'https://api.telegram.org/bot<MANAGER_BOT_TOKEN>/getUpdates' \
  --data-urlencode 'allowed_updates=["managed_bot"]'
```

在返回 JSON 的 `result` 数组中找到类似下面的字段，`managed_bot.bot.id` 就是 `<MANAGED_BOT_ID>`：

```json
{
  "update_id": 100,
  "managed_bot": {
    "user": { "id": 987654321 },
    "bot": { "id": 123456789, "username": "KIAgentBot" }
  }
}
```

上面的 `user.id` 是创建者的 Telegram 用户 ID，`bot.id` 才是 Managed Bot ID；实际响应还会包含更多字段。

然后调用：

```bash
curl -sS -X POST \
  'https://api.telegram.org/bot<MANAGER_BOT_TOKEN>/getManagedBotToken' \
  -H 'Content-Type: application/json' \
  -d '{"user_id": <MANAGED_BOT_ID>}'
```

其中 `<MANAGED_BOT_ID>` 是创建更新中 `managed_bot.bot.id` 的值；返回结果中的 `result` 就是 Managed Bot token。

如果没有保存这个 ID，可以用刚获取的 Managed Bot token 调用 `getMe`：

```bash
curl -sS \
  'https://api.telegram.org/bot<MANAGED_BOT_TOKEN>/getMe'
```

响应中的 `result.id` 是 `botId`，`result.username` 是 Managed Bot 用户名。

注意：`getManagedBotToken` 必须使用 Manager Bot token 调用；KI 扩展配置的是返回的 Managed Bot token。Bot token 冒号前面的数字是 **Bot ID**，不是 Telegram 用户的 `user_id`。

### 1.4 设置 Managed Bot 的访问用户

访问策略由 Manager Bot 调用 `setManagedBotAccessSettings` 设置，不需要在 KI 扩展中重复配置：

```bash
curl -sS -X POST \
  'https://api.telegram.org/bot<MANAGER_BOT_TOKEN>/setManagedBotAccessSettings' \
  -H 'Content-Type: application/json' \
  -d '{
    "user_id": <MANAGED_BOT_ID>,
    "is_access_restricted": true,
    "added_user_ids": []
  }'
```

- `is_access_restricted: true`：只有 Managed Bot 所有者可以使用。
- `added_user_ids`：额外允许的 Telegram 数字用户 ID，最多 10 个；所有者始终允许。
- 这是用户级访问控制，不是群组 ID 白名单，也不能按命令单独授权。

### 1.5 填写 KI 扩展配置

在 WebUI 的 `telegram-bot` 配置页填写 **Managed Bot** 的 ID 和 token。这里使用表单，不需要编辑 JSON：

- **Managed Bot ID**：填写 `<MANAGED_BOT_ID>`。
- **Bot Token**：填写 `<MANAGED_BOT_TOKEN>`。
- **回复模型**：可从模型选择弹窗指定；留空跟随全局默认模型。
- **思考强度**：在回复模型同一行选择该模型支持的 thinking effort；留空跟随模型默认。

不要填写 Manager Bot 的 ID 或 token。官方参考：[Managed Bots](https://core.telegram.org/api/bots/managed-bots)、[getManagedBotToken](https://core.telegram.org/bots/api#getmanagedbottoken)、[setManagedBotAccessSettings](https://core.telegram.org/bots/api#setmanagedbotaccesssettings)。

## 2. 安装并启用扩展

将本目录复制到：

```text
{KI_HOME}/extensions/telegram-bot
```

`{KI_HOME}` 默认为 `~/.ki`。运行 `ki serve` 的环境需要能够执行 `go`，扩展 runtime 使用 `go run .` 启动。

启动 KI 后，在 WebUI 的 **Settings → Extensions** 中启用 `telegram-bot`。扩展会在 server 监听成功后自动启动，不需要先创建 session。

## 3. 配置扩展

在对话页右上角的 `telegram-bot` chip 中，或 **Settings → Extensions → Configure** 中填写 Managed Bot 表单：

- `botId` 是 Managed Bot 的数字 ID，按字符串填写。
- `token` 是 Managed Bot 的 token，不是 Manager Bot 的 token。
- `model` 是 Telegram 回复模型，可在模型选择弹窗中选择；不选择时跟随全局默认模型。
- `thinkingEffort` 是回复模型的思考强度，下拉框就在回复模型同一行；留空跟随模型默认。
- 当前一个扩展实例接入一个 Managed Bot；不再配置 `accounts`、私聊/群组白名单或命令权限。
- WebUI 不会回显 token；已配置时 Token 输入框显示“留空保持不变”，直接保存即可。

Telegram 的用户访问策略由 Managed Bot 在 Telegram 侧控制。扩展不重复维护用户或群组白名单。

## 4. 使用

- 私聊：由 Telegram Managed Bot 访问设置决定是否可用。
- 群组：需要关闭 Privacy Mode 或将 Bot 设为管理员，才能收到所有群组消息。未 @ Bot 的消息只进入该群组 session 历史，不回复；明确 `@你的_bot` 的消息才触发 KI。扩展不维护群组白名单。
- 支持 `/new`、`/cd <path>`、`/compact`、`/reload`。命令不再配置独立权限，访问权限由 Telegram Managed Bot 策略决定。
- 每个 chat/topic 的 session 映射键为 `telegram:<accountId>:<chatId>:<threadId>`。**只有论坛话题才算独立会话**：Telegram 对群里任意一条"回复"都会建 message thread（线程 id = 被回复消息的 id，见 [api/threads](https://core.telegram.org/api/threads)），普通群里的回复线程并入该群原会话（`threadId` 记 `0`）；论坛（`getChat.is_forum`）与私聊 BotFather threaded mode（`getMe.has_topics_enabled`）才按 `threadId` 分会话。General 话题（`threadId=1`）同样算 `0`，发送时省略 `message_thread_id`——Telegram 对 `message_thread_id=1` 回 `message thread not found`。
- workspace 自动创建在 `{KI_HOME}/workspace/telegram/<accountId>/chat-<chatId>/topic-<threadId>`，不同 chat/topic 不会共用目录；注册时的显示名是 `group-<群名>`、`group-<群名>-<话题名|thread-id>`、`private-<@username|姓名|chatId>`（`session.create` 的 `workspaceTitle`，只在新建 workspace 时生效，之后你在 WebUI 里改的名字不会被覆盖）。论坛话题名只能从 `forum_topic_created` / `forum_topic_edited` 服务消息里学到并缓存在 `state.json`。
- 群里**回复 Bot 自己的消息**等同 @ 它：普通 `@` 和 reply 都会触发 run，未 @ 也未回复 Bot 的消息仍然只进历史。
- 群组消息会带简短的发送者名称和 `user_id`，用于区分多人发言。
- 未 @ 的群组消息通过 `session.appendMessage` 进入正常历史，不启动模型；@ 消息通过 `session.enqueue` 触发 prompt，并读取此前已提交的完整历史。
- 私聊或群组明确 @ Bot 的消息会尽力添加 `👀` reaction；未 @ 的群组消息不添加 reaction，只记录历史并确认处理。私聊使用 Telegram 的 30 秒临时草稿流式更新，并在结束时发送普通消息固化，群组使用占位消息编辑；不会发送 thinking、原始 tool call、参数和完整 tool result。
- 预览更新（草稿、工具状态、占位编辑）按每个 chat 约 1 秒的节奏写入，避免触发 Telegram 的限流；最后一条回复不受节奏限制，一定写入。
- 群组的最终回复就是那次占位消息编辑。编辑被限流（429）会按 `retry_after` 重试，仍失败则改发一条普通消息并删除占位，同时把失败原因打到 server stderr（`telegram-bot: edit final message: …`）——回复不会静默丢失。
- 如果模型或 Responses 流失败，扩展会清理已收到的 partial 文本，并在草稿/占位消息中显示失败原因，不会把 partial 当成正常回复。
- 普通 4xx 和 Responses 协议格式错误会立即返回，不会重复请求多次；429、5xx、网络错误和网关明确标记的瞬时 `bad_response_status_code` 仍会退避重试。

## 常见问题

- **没有回复**：确认填入的是 Managed Bot 的 token 和 ID，Telegram 侧访问策略允许当前用户，并确认群组消息 @ 了 Bot 或回复的是 Bot 自己的消息。
- **同一个群出现多个会话/工作区**：这是 Telegram 的 message thread 而不是论坛话题（`is_forum` 为假），现在普通群不再按线程分会话；老的 `topic-*` 工作区可以手动改名或删除。
- **群里只留下 `…` 或半截回复**：最终编辑没写进去。现在会重试并在失败后改发新消息，同时 server stderr 会有 `telegram-bot: edit final message: …`；如果一条日志都没有，看 session jsonl 确认该轮是否真的跑完（`message` 里应有 assistant 回复）。
- **无法加入群组**：在 BotFather 中检查 `/setjoingroups`；是否能添加 Bot 仍受群组成员管理权限影响。
- **保存后未连接**：检查 server 日志和 Extensions runtime 状态；Telegram API 网络错误会自动重试。
