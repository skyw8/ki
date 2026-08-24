# Queue & Steering

忙时第二条用户消息不再 409。对照 Codex（`/data/hgy/codex`）的 `turn/steer` 与 `thread/queue`，ki 只扩展现有 `POST /v1/sessions/{id}/prompt` 和 session GET/PATCH。

已落地的契约写在 `docs/architecture.md`、`docs/session.md`、`docs/webui.md`。本文件是实现清单；完成后勾选。

## 行为

空闲：`POST prompt` → 202 `accepted: "started"` → occupy → `runPrompt`。

忙时默认 + 单次 `delivery` 覆盖：

| 模式 | 含义 |
|---|---|
| `steer` | 插入当前 `loop.Run`：当前模型 HTTP 流结束后追加 user，再请求。不新 occupy，不重新 Prepare MCP。 |
| `queue` | 写入 session `queue.json` FIFO，当前 run `release` 后再 occupy 新一轮。 |

缺省 `steer`（`toggles.json` `message.busy`）。Steer 不是 Completions / Responses / Anthropic 的厂商协议。

仍 409：`parentId` 且 busy；非 `/reload` 的 slash 且 busy。compact 占用不能 steer，降级 queue。

## WebUI：queue 默认时 Enter 入队，Ctrl+Enter 提升

Composer 忙碌时不再把 Enter 一律当成 abort。

| 操作 | `message.busy=queue` | `message.busy=steer` |
|---|---|---|
| Enter + 有内容 | 不带 delivery（默认 queue） | 不带 delivery（默认 steer） |
| 发送按钮 + 有内容 | 不带 delivery（默认 queue） | 不带 delivery（默认 steer） |
| Ctrl+Enter + 有内容 | `delivery=steer`（草稿插入本轮，不动 queue） | `delivery=steer` |
| Ctrl+Enter + 空 + `queued[]` 非空 | `{ delivery: "steer", queueId: 队尾 }` | 同左 |
| Enter / 停止 且输入为空 | abort | abort |
| Ctrl+Enter 且输入为空 | 提升队尾（有排队时） | 不 abort |

忙碌时 **停止和发送同时在**：有草稿才能发；空草稿只停。Edit 仍 409（`parentId`）。

`queueId` 走现有 `POST prompt`，不新开路由。服务端 `TakeQueueID` 后写入捕获的 occupy Inbox；该 occupy 已结束则 `EnqueueFront`（compact 无 Inbox 时 `202 queued`）或空闲时新 occupy。

## 事件（给 SSE / jsonl / 以后 hook）

不给三种操作各发明平行的空类型。只补现在缺的、订阅者用得上的：

| 事件 | 何时 | 落盘 | 用途 |
|---|---|---|---|
| `steer_accepted` | `Inbox.Push` 成功 | 只进当前 run SSE（`st.evs`），**不**进 jsonl 树 | 立刻渲染将插入本轮的 user；abort 未 drain 则不会有后续 `message_end` |
| `run_aborted` | `POST /abort` 的 `st.cancel()` | sideband jsonl + 当前 run SSE + `?notifications=1` | 「已请求停止」；`busy` 仍等到 `agent_end` 才清（工具可能还在杀） |
| `queue_changed` | 入队 / 出队 / PATCH 删 | 已有 sideband | 刷新 `queued[]` |
| user `message_*` | steer drain 或新一轮 queue 开跑 | 对话树 | 模型可见、气泡落盘 |

Drain 后的 steer 仍发普通 `message_start/end`（带 `entryId`）。前端用 `steer_accepted` 做乐观气泡，再用 `message_end` 换成正式节点。

没有 `abort` 替代 `agent_end`。queue → steer 不是新事件：提升发已有的 `queue_changed` + `steer_accepted`。

## 与 Codex 的差异

| Codex | ki |
|---|---|
| `turn/steer` + `thread/queue/*` | `POST prompt` 的 `delivery` |
| SQLite、reorder | `queue.json`、最多 100、删、不重排 |
| interrupt 暂停队列 | abort 后仍出队 |
| TUI 内存队列 + durable | 一套 session queue + 同轮 steer |
| Enter=steer、Tab=queue | Enter 走默认；queue 时 Ctrl+Enter 提升队尾或 steer 草稿；无 Tab 绑定 |

## 清单

- [x] `loop.Inbox`、toggles、prompt `delivery`、queue.json、CLI、设置 Message 页签
- [x] Composer：busy 时停止+发送；Enter 走默认；Ctrl+Enter 提升队尾或 steer 草稿
- [x] App `sendContent` 可传 delivery；Ctrl+Enter 空输入 POST `queueId`
- [x] `steer_accepted` 进当前 run SSE（含 message）
- [x] `run_aborted` sideband + SSE
- [x] WebUI `applyEvent`：`steer_accepted` 乐观 user；`run_aborted` 可记 stopping
- [x] 测试：busy+queue 默认时 prompt 不带 delivery 入队；带 steer 插入 Inbox；`queueId` 提升队尾
- [x] e2e：假模型 `e2e-hold` 卡住一轮；HTTP 与 Playwright Enter 入队、Ctrl+Enter 提升队尾
- [x] `docs/webui.md` / `docs/architecture.md` 事件表
