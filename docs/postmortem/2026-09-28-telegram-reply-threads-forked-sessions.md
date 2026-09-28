# Telegram 回复串被当成论坛话题：同一段对话裂成两个会话

日期：2026-09-28  
范围：`extensions/telegram-bot`（inbound 路由：`externalKey` / `sessionFor` / `addressed`）

## 现象

同一天里第二次出现"Telegram 里没人回"。这次服务端一切正常，但用户在群里**回复** Bot 的答案后：

- ki 里多出一个 `topic-145` 会话和工作区，而原来的会话是 `topic-0`（`workspaces.json` 里两个都叫 `topic-*`，肉眼分不清）；
- 那条回复在原会话里没有上下文，在 `topic-145` 里也没被回答；
- 用户以为自己在同一个群聊里继续同一段对话。

## 证据

| 事实 | 来源 |
|---|---|
| 回复带 `threadId=145`，11 秒后同一句话又以 `threadId=0` 出现在 General | session `01a0e8b2…`（`messageId=148`、update `…795202673`）与 `01a04c7b…`（`messageId=149`） |
| 这个群**不是论坛** | `getChat(-1004449407453)` 未返回 `is_forum`；`getMe.has_topics_enabled=false` |
| Bot API 没有查询话题名的方法 | `getForumTopic` 返回 `404 Not Found` |
| 145 是什么 | Telegram 文档 [api/threads](https://core.telegram.org/api/threads)：*"Threads are usually automatically created when replying to any message in a group. All replies to a message with ID 420 are associated to thread with ID 420"* —— 线程 id = **被回复消息的 id**，任意群、任意回复都会建 |
| 那条回复没触发模型 | 该 session 只有 `context-queue.json`（`next=1, items=[]`，说明已作为历史落进 transcript）、没有 assistant 消息、没有 `ext-queue.json` |

## 根因（两个独立问题叠在一起）

1. **把 `message_thread_id` 无条件当会话维度**：字段名里的 "thread" 不是产品概念里的 "topic"。论坛话题确实是独立会话，但普通群里每回复一条消息 Telegram 都会建一个 thread（且线程 id 随被回复消息变化，回复不同消息 = 不同 id），于是随手一个"回复"就把同一段对话劈成两个 session + 两个 workspace。同类网关也踩过：Hermes #13195 把这种 id 当 session 分区，用户看到"模型配置在一处改了、下一句却在另一个会话里执行"。
2. **群里只认 @mention 才算"叫它"**：`addressed := !group || mentionsBot(...)`，而解析出来的 `message` 结构里根本没有 `reply_to_message`，所以"回复 Bot"既不算呼叫、也不会回复，只静默进入历史。

## 修法

- `sessionThread(chat, threadID, botTopics, forum)`：只有论坛（`is_forum`，缺值时用一次 `getChat` 探测并缓存）和私聊 BotFather threaded mode（`getMe.has_topics_enabled`）才按 thread 分会话；普通群的回复线程 `threadId` 归 `0`，并入该群原会话。
- General 话题在 API 里是 1、在 update 里是 0：两者都归 `0`，且发送时省略 `message_thread_id`（Telegram 对 `message_thread_id=1` 回 `message thread not found`，tdlib #798 / openclaw 都记录过）。
- `repliesToBot(msg, me)`：回复 Bot 自己的消息等同 @ 它，触发 run 而不是只进历史。
- 顺带让会话可辨认：`session.create` 支持 `workspaceTitle`（只在首次登记 workspace 时生效，不覆盖 WebUI 改名），扩展传 `group-<群名>[-<话题名|thread-id>]` / `private-<@username|姓名|chatId>`；话题名只能从 `forum_topic_created`/`forum_topic_edited` 服务消息里学并缓存在 `state.json`。

## 结论

- **平台字段名不等于产品概念**：`message_thread_id` 的语义是"这条消息属于哪个回复串"，不是"哪个论坛话题"。把外部 id 当会话维度之前，先确认它在平台里到底代表什么。
- **同一句抱怨可能对应多个根因**：当晚"Telegram 没回复"先后有三条独立原因（丢 run 身份、最终 edit 静默失败、回复被路由到另一个会话）。先看 `state.json` 的 session 映射和 workspace 目录名，能最快发现"其实进了另一个会话"这一类。
- **命名即诊断**：`topic-0` 这种由路径 basename 兜底的显示名让两个不同会话长得一模一样；可读的 workspace 标题（`group-怀仁堂`）本身就是排障信息。
