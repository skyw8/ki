# 被一个卡住的读者钉住的 run 回放；折叠视图里消失的提问

日期：2026-09-27  
范围：`internal/server`（run 回放缓冲）、`internal/session/view.go`（对话窗口）、`internal/cli`（流式打印）、`web/src/App.tsx` + `web/src/features/markdown/Markdown.tsx` + `web/src/lib/messageView.ts`（重放渲染与折叠）

## 现象

1. **重新进入正在运行的会话要等很久**（分钟级），期间页面基本不动。
2. **重新进入会话时，compact 视图里有时只有一条折叠行 + 最近一条消息，没有该 turn 的用户消息。**
3. **正在运行的 turn（以及重新进入正在运行的 turn）不折叠**，整轮的工具往返全部渲染，加重上面的慢。

## 测量

- 125 个有窗口的线上会话：**43 个的默认窗口里一条 user entry 都没有**；82 个窗口的首个节点不是 user。
- 用真实模块跑 compact 渲染（窗口起于 turn 中间的 7 个节点）：`turns = [{id: "a1", user: null, nodes: 7}]` → `["fold:a1", "a4"]`。
- 一个 live run 的 SSE 缓冲：新 reader 首 0.5s 收到 **4145 条事件 / 144 MB**；随后一次测量 **14766 条 / 286 MB**（其中 14561 条 `message_update`，均值 24 KiB，各带整份累积文本）；事件序号已到 30852；daemon RSS **~1.0 GB**。
- 同一 daemon 的一条 SSE 连接 **send-queue 积压 3.1 MB**：一个不再读取的客户端（后台标签页 / 被挂起的手机页 / 停掉的端口转发）。
- 折叠差：一个在跑的 turn 有 61 个回复节点，`busy` 豁免下整轮渲染；折叠后 2 行。
- 客户端：每个 SSE 事件一次 `setView`（每个事件一次 React 渲染），流式消息在 streaming 模式下 `parseMarkdownIntoBlocksFn={undefined}`，每次渲染都重新切分整段 markdown。

## 根因

1. **窗口按条数切**（`selectLeafEntries`: `tailStart = len(path) - limit`），与 turn 边界无关；`groupTurns` 只在 user 节点处开新 turn，`foldReplies` 对无 user 的 turn 只画折叠行 → 首个 turn 的提问从来没被加载过，不是渲染 bug。
2. **`foldReplies` 的 `busy` 豁免**（`opts.busy && i === turns.length-1 ? 0 : ...`）让运行中的最新 turn 整轮展开 —— 而那个 turn 通常最长。
3. **裁剪只看 reader 位置**：`trimLocked` 只在 `idx < minReaderPos()` 时清空被取代的 payload，于是一个停住的 reader 把裁剪点永久钉死；同时新 reader 的 `pos` 从 0 开始（`reader := &runReader{}`），**每次重连都重放整个缓冲**。两个效果互相放大：重放期间这个新 reader 又在 pos 0 压住裁剪。
4. 客户端把这段重放逐条应用、逐条解析。

## 修复

- `internal/session/view.go`：`BuildTail` / `BuildBefore` 用 `withTurnOpeningUser` 额外带上开启窗口首个 turn 的 user message。`hasMore` / `oldestId` 语义不变（仍是窗口自己的边界），翻页照旧逐条向前，所以被跳过的那段中间内容仍然可达（多一条 entry，不扩大窗口）。
- `web/src/lib/messageView.ts`：删掉 `busy` 豁免，运行中的 turn 照常折叠；`isLive` 继续保活流式文本与运行中的工具。
- `internal/server/server.go`：裁剪增加上限（最新 128 条过时 payload、总量 4 MiB），超出后不再为落后的 reader 让路。安全性：最新 partial 已经带整份累积文本，更早的内容在 `GET /v1/sessions/{id}` 的 transcript 里，落后 128 条以上的读者本来也看不到直播。
- `internal/cli/cli.go`：`streamPrinter` 改成打印累积 partial 里尚未输出的后缀（而不是直接打 `d.Delta`），所以裁剪和重连都不会让 CLI 漏字。
- `web/src/App.tsx`：run 事件入队、按 16ms 合并成一次 `setView`，连续 `message_update` 只留最后一条；游标仍在同一个 updater 里推进。
- `web/src/features/markdown/Markdown.tsx`：`useStreamingText` 给流式渲染加渲染节流（最后一次一定渲染；见 [流式正文冻在 debounce 后面](2026-09-27-streaming-text-froze-behind-a-debounce.md)），随后改成只解析定稿前缀之外的尾部（见 [webui.md](../webui.md)）。
- 测试用假 provider 新增 `e2e-stream-<n>`（每 chunk 一整行 markdown、重复整份 partial），用来复现重放/渲染路径。

## 验证

- Go：`TestBuildViewKeepsTheTurnOpeningUser`（窗口带提问、`oldestId` 不变、翻页仍能取回中间内容）；`TestEmitterTrimsBehindAStalledReader` / `...AcrossMessages`（stalled reader 下保留量有界）；`TestEmitterKeepsDeltasALiveReaderIsOwed` 保留在阈值内的语义；CLI 新增 `TestStreamPrinterFillsSkippedChunks` / `...ResyncsWhenThePartialIsNotExtended`。
- WebUI（fake provider）：`a running turn folds its replies like any finished turn`（运行中 `fold-row=1`、`assistant-message=0`、`tool-card=0`，`composer-stop` 同时可见）；`a burst of streaming chunks keeps the main thread responsive`（400 chunk 时 long task 总量 91ms、最长 91ms、1 个）；`foldReplies folds the running turn too, keeping live work visible`；`foldReplies` 单测其余部分不变。
- `bun run test:e2e` 136/136、`go test ./...`、`tsc`、`go vet`。

## 教训

- **不要把「不丢读者需要的数据」和「一个卡住的读者可以拖垮所有人」混成一条不变量。**「按 reader 位置裁剪、不按定时」本身是对的，但它没有上限：现实中确实会有人不再读取（后台标签页、挂起的手机、断掉的转发），于是内存和重放开销都随「最慢的读者」无限增长。约束要有界。
- **重放和直播要分开量。** 第一次测量把 30 秒的直播流量当成了重放（5.4 MB/970 条），数字对不上；分开 burst 与 steady 之后才看到 286 MB 这一量级。
- **客户端的「慢」可能是每来一条就整段重算。** 14k 条事件 × 每条全量解析，和服务端 286 MB 是同一件事的两端；两边都要修。
- **一个 bug 会掩盖另一个。** 窗口缺 user 消息、live turn 不折叠都一直在，只是重新进入慢到无法忍受时才被发现；`?before=` 白拉一页那个老问题此前也掩盖过 follow-tail 的顺序错误。
- **下限也要测。** 折叠/上限这类改动要正反都验：把上限调回默认、把 `busy` 豁免加回去，新用例必须失败。
