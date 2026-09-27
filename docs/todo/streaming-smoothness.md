# 流式输出流畅度：Codex 源码调研与 Ki 优化方案

日期：2026-09-27。状态：核心方案已实现；第 8 节记录代码、实测与外部验收边界。第 1–6 节保留实施前调研与目标，不能把其中的旧链路描述当作现状。

## 1. 结论与范围

Ki 下一步应优先优化 **收到内容到显示内容之间的延迟，以及每次更新的工作量**，而不是继续增加节流时间或更换 SSE。建议顺序：建立分层测量 → 统一显示调度、消除饥饿 → 降低全文编码和全窗口更新成本 → 完善增量 Markdown 与定稿交接 → 补齐弱网恢复。

Codex 值得借鉴的组合是：有身份的文本增量、独立且不会被新 delta 推迟的显示时钟、按积压年龄追赶、稳定正文与活动尾部分离，以及复杂语法的保守回退。它没有通过无限排队的打字机动画制造流畅感。

“没有 streaming stall”应定义为：**前台客户端已有可显示的新内容时，不因自身排队、解析或渲染而长时间停止更新；模型停止产出时，准确呈现等待状态。** 客户端无法消除模型思考、工具运行或网络中断造成的真实停顿。

调研基线：

| 仓库 | 本次读取版本 | 范围 |
| --- | --- | --- |
| `/data/hgy/codex` | `8f195c93d7e7acfef95acf273f0e49cce917e291`，读取时工作区干净 | Rust provider/core/app-server 协议及 **TUI**；没有验证 Codex 桌面应用内部的 Web 渲染实现 |
| `/data/hgy/ki` | `cad0439156afd616a9b21688b0df0d614657c20e`，调研开始时工作区干净 | provider → loop → SSE → WebUI/CLI，以及现有复盘、测试和性能方案 |

这是本地源码对照、Ki 微基准与既有回归检查，不是两个产品在同一模型上的端到端性能对比。下文将源码事实、测量结果、待验证假设分开列出。

## 2. Codex 怎样保持持续可见的输出

### 2.1 传输的是带身份的增量

- [`codex-api/src/sse/responses.rs`](/data/hgy/codex/codex-rs/codex-api/src/sse/responses.rs) 将 `response.output_text.delta` 直接转换为 `ResponseEvent::OutputTextDelta`，通过容量为 **1600 个事件**的 Tokio channel 交给消费者；独立任务读取 SSE。队列满时仍会背压，这不是“永不阻塞”的保证，也不是按字节的内存上限。
- [`core/src/session/turn.rs`](/data/hgy/codex/codex-rs/core/src/session/turn.rs) 的 `OutputTextDelta` 分支依据活动 item 解析并发出文本增量。
- [`app-server-protocol/src/protocol/event_mapping.rs`](/data/hgy/codex/codex-rs/app-server-protocol/src/protocol/event_mapping.rs) 把它映射为 `AgentMessageDeltaNotification`；[`v2/item.rs`](/data/hgy/codex/codex-rs/app-server-protocol/src/protocol/v2/item.rs) 的正文增量字段是 `threadId / turnId / itemId / delta`，无需每帧从两份 JSON 全文反推出字符串后缀。

可迁移原则：让“哪条消息的哪个内容块发生什么变化”尽早明确，同时保留权威终态。Ki 已经有 wire patch，差距主要在生成 patch 的工作量，而非是否使用增量传输。

### 2.2 显示时钟独立于 delta，连续输入不会推迟 deadline

[`tui/src/app/event_dispatch.rs`](/data/hgy/codex/codex-rs/tui/src/app/event_dispatch.rs) 处理 `StartCommitAnimation` 时用 `get_or_insert_with` 创建 interval，重复事件不会重启时钟；错过 tick 使用 `MissedTickBehavior::Delay`，不补打一串历史动画帧。

当前 checkout 的 `COMMIT_ANIMATION_TICK` 来自 `TARGET_FRAME_INTERVAL`，与 [`frame_rate_limiter.rs`](/data/hgy/codex/codex-rs/tui/src/tui/frame_rate_limiter.rs) 的 **8,333,334ns，约 120Hz 上限**一致。这个数字是该版本 TUI 的调度参数，不代表实际输出恒定 120 FPS。

[`frame_requester.rs`](/data/hgy/codex/codex-rs/tui/src/tui/frame_requester.rs) 合并重绘请求，选择已有与新请求中较早的 deadline，再按上次绘制时间限速。值得迁移的是“不推迟已有期限、合并无意义绘制”，不是把浏览器也硬设为 120Hz。

### 2.3 有积压就追赶，不维持越来越落后的动画

[`streaming/chunking.rs`](/data/hgy/codex/codex-rs/tui/src/streaming/chunking.rs) 是纯调度策略：

| 状态/条件 | 当前参数与动作 |
| --- | --- |
| Smooth | 每个 commit tick 提交一行 |
| 进入 CatchUp | 队列 ≥8 行，或最老一行等待 ≥120ms |
| CatchUp | 本次排空当前行队列 |
| 退出条件 | 队列 ≤2 行且最老等待 ≤40ms，保持 250ms；队列为空直接恢复 Smooth |
| 防抖动 | 退出后 250ms 内抑制再进入；≥64 行或 ≥300ms 严重积压可绕过冷却 |

[`commit_tick.rs`](/data/hgy/codex/codex-rs/tui/src/streaming/commit_tick.rs) 采集真实队列长度、最老年龄并执行 drain，还记录模式转换。除了周期 tick，[`chatwidget/streaming.rs`](/data/hgy/codex/codex-rs/tui/src/chatwidget/streaming.rs) 在收到 delta 后调用 `run_catch_up_commit_tick`，无需等下一个普通动画周期才处理积压。

Ki 不必建立逐行播放队列。对应做法是：**同一消息用最新已解码版本追赶显示，不能为了平滑把已经收到的内容慢慢播放几秒钟。**

### 2.4 稳定正文、可变尾部与即时预览分开

- [`streaming/render.rs`](/data/hgy/codex/codex-rs/tui/src/streaming/render.rs) 保留稳定源文本边界和已渲染行边界，只重算最后一个可能变化的顶层块。
- [`markdown_stream.rs`](/data/hgy/codex/codex-rs/tui/src/markdown_stream.rs) 负责原文积累与换行提交；[`controller.rs`](/data/hgy/codex/codex-rs/tui/src/streaming/controller.rs) 另有未换行文本的 preview。因此不能只看 collector 就得出“Codex 要等换行才显示字”的结论。
- [`prose_preview.rs`](/data/hgy/codex/codex-rs/tui/src/streaming/prose_preview.rs) 增量扫描新字节，普通未结束行只格式化末尾 **8192 字节**的预览；它不推进稳定提交边界。未闭合链接目的地址被暂缓显示，避免长 URL 在闭合时突然收缩成标签。
- 表格、缩进、引用等有语法歧义的内容会暂缓提交；preview 也不是所有语法都立刻展开。
- [`code_fence.rs`](/data/hgy/codex/codex-rs/tui/src/streaming/code_fence.rs) 对符合条件的顶层代码围栏增量追加高亮行；疑似闭合行、主题变化、归一化差异等情况退回标准解析。

需要同时学习其回退：reference definitions 会影响整篇文档，`render.rs` 为此全文重算；宽度变化和 `finalize_remaining` 也存在全文重算。不能将 Codex 描述成任何输入都严格 O(delta)，也不能照搬其截取预览尾部的策略，导致 Ki 用户看不到已接收正文。

### 2.5 检测失联与平滑显示是不同问题

Codex SSE 读取有 `timeout(idle_timeout, stream.next())` 和 telemetry；默认 idle timeout 在 [`model-provider-info/src/lib.rs`](/data/hgy/codex/codex-rs/model-provider-info/src/lib.rs) 中为 **300,000ms**。它用于判定异常等待，不用于填补 token 空档，也不能作为 300ms 显示延迟目标的实现。

## 3. Ki 已经完成的优化与剩余开销

### 3.1 已完成，实施时应保留

当前代码已超过[既有滚动与性能方案](webui-transcript-scroll-and-performance.md)早期调研的状态，应结合其“实施结果”阅读，不能把早期问题表当作当前事实。

| 已有机制 | 代码/契约 |
| --- | --- |
| 逐事件 Flush、SSE 不压缩、15s 注释心跳 | [`server.go`](../../internal/server/server.go) 的 `events`、[`push.go`](../../internal/server/push.go)、[WebUI 契约](../webui.md) |
| 慢 reader 不再无限钉住过时 payload | `runState.trimLocked`：128 条及约 4MiB 的过时 payload 保留限制，最新 partial 另计 |
| 快照 + 连接内 patch；重连获得新快照 | [`loop/wire.go`](../../internal/loop/wire.go)、[`messageStream.ts`](../../web/src/api/messageStream.ts)、[事件契约](../events.md) |
| patch 先解码，再合并完整消息 | [`client.ts`](../../web/src/api/client.ts) 与 [`App.tsx`](../../web/src/App.tsx)；16ms 批次合并连续 `message_update`，游标跟随 reducer 提交 |
| 持续流不再被 debounce 冻住 | [`Markdown.tsx`](../../web/src/features/markdown/Markdown.tsx) 的 `useStreamingText` 按上次执行时间算 80ms deadline |
| Markdown 稳定段封存、长活动尾部先显示源码 | [`streamText.ts`](../../web/src/features/markdown/streamText.ts)；`SEAL_MIN`、`TAIL_MAX` 均为 4096，代码实际按 JS `string.length`，即 UTF-16 code units，不是 UTF-8 字节 |
| 历史虚拟化、按需正文、解析队列和缓存 | [`Chat.tsx`](../../web/src/features/chat/Chat.tsx)、[`parseQueue.ts`](../../web/src/features/markdown/parseQueue.ts)；已有滚动意图和锚点契约 |
| CLI 从累积 partial 打印未输出后缀 | [`cli.go`](../../internal/cli/cli.go)，允许裁剪与跳过中间更新而不漏字 |

两篇复盘是本方案的回归基线：[debounce 冻结](../postmortem/2026-09-27-streaming-text-froze-behind-a-debounce.md)、[慢 reader 钉住回放](../postmortem/2026-09-27-run-replay-pinned-by-a-stalled-reader.md)。它们描述的修复已在当前代码中，不能再列为未完成任务。

### 3.2 源码确认的成本，以及尚未证实的因果

| 位置 | 源码事实 | 判断 |
| --- | --- | --- |
| `MessageEncoder.Encode` | 每个 update 对完整 Message 做 marshal → unmarshal → `reflect.DeepEqual`/递归 diff，再 marshal patch；每个 SSE reader 各执行一遍 | wire 字节已减小，CPU/分配仍依赖累积正文长度；微基准见下节 |
| provider 累积正文 | `llmprotocol/http.go`、`responses.go`、`anthropic.go` 仍使用 `Text += delta` / `Thinking += delta` | Go 字符串增长也可能带来重复复制；尚未量化其在真实 profile 中的占比 |
| `events` 与 loop | 编码和网络写在 SSE handler 中，取 event 后已释放 `st.mu`；loop 的 emit 与 provider 读取回调同步 | 不能直接声称慢 SSE 写锁住 provider；需分开测 reader 排队、共享 CPU/GC 和 emit 锁等待 |
| App → Markdown | 先 16ms 批次，再独立 80ms 正文节流；80ms 对应约 12.5 次/秒的显示节奏 | 修复了无限防抖，但未形成统一的最大显示延迟约束；两层定时和 React/布局可能增加等待 |
| `normalizeMarkdown` / `settleBoundaries` | 每个 shown 版本归一化全文，并从头寻找边界；封存 effect 也检查既有段前缀 | AST 分段并不等于所有工作都增量化；扫描成本已做局部测量 |
| reducer / Chat 派生 | `applyEvent` 复制 nodes/records/requests；update 映射 records，`updateRequest` 再映射 requests/records；Chat 的若干 `useMemo` 仍依赖整个 nodes | 历史对象身份已有优化，活动 delta 仍能触发窗口级遍历；是否主因需要 React profile |
| 突发解码 | `sse()` 对同一 read 内所有行 parse/yield，没有显式主线程时间预算 | 大 burst 可能通过连续 microtask 推迟 timer/rAF；这是待浏览器 trace 验证的饥饿风险 |
| 消息结束 | `applyLiveMessage` 将临时 id 改为 entryId；列表 key 取 item id；Markdown 转静态分段/排队 | 存在重挂载、重新排队和布局变化路径，需量测“结束瞬间卡顿/闪空”，不能只测 streaming 中段 |
| 解析队列 | 每帧放行一个任务，而非限制一次 React parse 的实际耗时 | 单个大块仍可超过一帧；rAF 排队不是可抢占解析 |
| 超时/心跳 | 默认 protocol client 使用 `http.DefaultClient`，`postStream` 未实现显式读取 idle watchdog；SSE handler 未设置每次写 deadline | 上游静默和下游坏链路需单独治理；15s 下游 ping 不代表上游仍在产出 |

另一个正确性检查点：静态分段路径会检查 reference definitions，但流式封存路径没有同样的文档级失效处理。应增加跨段引用回归，确认后再修复，不能默认“空行之后的 Markdown 永远不受后文影响”。

## 4. 本次测量与验证

### 4.1 Patch 编码微基准

环境：Linux amd64，Xeon Gold 6240R，Go 1.26.6。临时 module 使用 `replace ki => /data/hgy/ki` 导入真实 `loop.MessageEncoder`；没有修改生产源码。

方法：预生成 128 个单 text block 的 ASCII 累积 Message，初始正文分别为 8/64/256/1024KiB，每帧追加 64B；`testing.Benchmark` 循环调用 `json.Marshal(encoder.Encode(event))`，每 128 帧重置 encoder，包含首快照的摊销成本。输入构造在计时外。不含 provider、网络、CLI 解码和浏览器。

| 初始正文 | 平均编码 + JSON 输出 | 分配量/帧 | 分配次数/帧 |
| --- | ---: | ---: | ---: |
| 8KiB | 0.205ms | 29,417B | 62 |
| 64KiB | 1.145ms | 153,418B | 62 |
| 256KiB | 3.062ms | 556,210B | 62 |
| 1024KiB | 11.827ms | 2,153,847B | 62 |

结果证明了“增量 wire 仍付出与全文长度相关的编码成本”。例如 256KiB、50 帧/秒、单 reader 时，按这组均值推算仅该路径约产生 **26.5MiB/s 分配**；这是算术外推，不是线上 CPU/GC 实测。多 reader 和正文内容会改变结果。

### 4.2 前端全文扫描微基准

Bun 1.3.14，调用真实 `normalizeMarkdown` + `settleBoundaries`，以包含 CRLF、空行和粗体标记的重复段落构造指定长度，预热 20 次、采样 100 次。

| 原始 ASCII 文本长度 | p50 | p95 |
| --- | ---: | ---: |
| 8KiB | 0.067ms | 0.084ms |
| 64KiB | 0.397ms | 0.433ms |
| 256KiB | 1.261ms | 1.428ms |
| 1024KiB | 4.981ms | 5.516ms |

这些数字不含 Streamdown、React 和 DOM，不能当作手机或 Chromium 帧耗时。它说明扫描依然增长，但**不能据此认定扫描就是当前唯一或最大的卡顿来源**。实施前应将这两种探针固化为性能基准，并补充多内容块、大工具参数、中文及非重复正文。

### 4.3 本次运行的现有回归

- `go test ./internal/loop ./internal/server ./internal/cli -run 'Test(MessageWire|EventsWire|Emitter.*(Trim|Replay|LiveReader)|StreamPrinter)' -count=1`：三个包通过。
- `cd web && bunx playwright test --project=fake e2e/markdown-stream.spec.ts e2e/message-stream.spec.ts e2e/live-streaming.spec.ts`：**12/12 通过，17.3s**，使用隔离的测试服务。

现有“keeps the text moving”在 6 秒内采样并要求至少 5 个不同长度，能抓完全冻结，但不足以排除中间几百毫秒甚至秒级的停顿。`live-streaming.spec.ts` 在本次命令中也是 fake project 下的 reducer 回归，不是真实模型验证。本次没有运行真实 provider、弱网或移动真机 A/B，也没有运行 Codex 性能测试。

## 5. 实施方案

### M0 / P0：先建立可归因的 stall 指标

沿已有事件流记录阶段，不新增 REST 路由：

| 阶段 | 指标 |
| --- | --- |
| provider | 首个有效增量时间、相邻内容增量间隔、上游原始字节活动时间、emit 回调耗时 |
| server reader | enqueue→取出年龄、编码耗时/字节、write+flush 耗时、积压量、快照/patch 比例 |
| browser decode | 收到字节时间、最后成功解码 revision、解码批次耗时、待应用事件数 |
| browser display | 最老未显示内容年龄、已显示 revision、React commit 耗时、正文可见更新间隔、追赶次数 |

本地阶段用单调时钟；不能直接用浏览器 `performance.now()` 减服务端 Unix 时间算单向延迟。关联使用 runId、seq 和消息身份，日志仅记录长度、时间和阶段，不记录正文或凭据。服务端先用采样 tracing/测试探针，浏览器扩展已有 `PerfHud`；需要持久化的汇总走现有 loop.Event/jsonl 契约。

至少区分：上游没有新增内容、网络没有到达、客户端收到但未应用、应用后尚未显示。将 **pendingAge** 定义为“最老尚未被屏幕版本覆盖的可显示更新已等待多久”，不能在每次收到更新时重置它，否则连续输入会再次掩盖饥饿。

### M1 / P1：统一显示调度，保证文本先可见

把 `App.listen` 中的调度抽成可独立测试的 session/run 控制器，Markdown 不再额外持有一套通用的 80ms 文本限速。保持三个进度：`decodedSeq`、`appliedSeq`、`displayedRevision`；重连游标跟随成功应用状态，不能跟随动画进度，也不能跳过尚未应用的事件。

建议初始策略，参数由 M0 结果调整：

1. **顺序解码，合并显示。** 所有 patch 按 baseSeq 还原成功后，才允许同一消息的连续 update 合并为最新快照；message/tool/turn 边界维持 FIFO，不能跨边界覆盖或丢弃。
2. **前台由一个 rAF 驱动提交。** 首个可见增量争取下一帧显示；普通更新以 16–33ms 为初始显示周期。已有最早 deadline 不被后续 delta 推迟；低速输入不必等凑字数或换行。
3. **超过预算立即追赶。** 最老未显示更新达到 100–120ms 时，下一次可执行提交显示最新已解码文本，暂停非必要的富文本加工；不逐个播放旧版本。浏览器每次仍遵守工作预算，不能照搬 TUI“一次排空所有重渲染”的做法。
4. **解码也让出主线程。** burst/replay 按约 4ms 或最多 128 事件让出一次 task；保留 decoder 状态并按序继续。`await Promise.resolve()` 仍在 microtask 中，不是充分的让出。生命周期事件不丢弃，不要求单个超大 `JSON.parse` 可被中断。
5. **文本显示与格式化使用不同优先级。** 在已有稳定富文本后保留便宜、完整可读的活动文本层；长块优先追加源码，语法解析、高亮、图表进入有预算的队列。不要把整个流更新都放进可能被不断延后的 transition，也不要每帧闪换整篇正文。
6. **结束是屏障。** message_end、错误、中止、agent_end 保序，最终收到的权威正文立即进入显示状态，不能还等动画队列；昂贵格式化随后渐进完成。切会话/分支时取消旧调度并核对 generation，旧 timer 不能写新视图。
7. **后台不承诺帧率。** 隐藏时可以继续解码并合并可替换更新；不可替换边界事件超过预算时，应主动中断并依现有快照/游标契约恢复，不能无限积压。恢复可见后先追赶、再做格式化，不把后台 timer 节流计为前台 stall。

rAF、timer 都无法抢占已运行的 JS。deadline 是可观测的目标，真正保证需要同时降低下述单次工作量；增加一个 watchdog timer 本身不能修复长任务。

### M2 / P1：去掉热路径里不必要的全文工作

**服务端分两步做，不把“typed diff”误写成严格 O(delta)：**

- 第一步重构 `MessageEncoder`：保留上一份有类型、不可变的消息基线，直接按 Message/Content 字段生成现有 append/set/remove/resize 操作，去掉 JSON 往返及反射比较。文本前缀比较仍然依赖长度，但可显著减少分配。基线若引用可变 map/slice，必须复制或有明确的不可变所有权，不能用共享可变消息冒充快照。
- 第二步由 profile 决定是否将 typed change 向 provider/loop 前移。当前 `AssistantDelta.Delta` 没有足够统一的内容块位置，不能把它盲目拼到最后一段 text。需要内容块身份/索引、操作类型、消息 revision，并覆盖 thinking、工具参数、块增删、替换与重置；内置 provider 和 extension provider 都须有明确契约。
- 对可信 append 操作可直接生成 patch；provider 改写、未知操作或恢复基线缺失时发快照。原始增量日志应有字节/数量预算：reader 的基线落在保留区间内才合成 patch，否则回到完整快照。连接自己的 baseSeq/messageStream 语义不变。
- 第二步才评估 provider 内部 chunk/builder accumulator，避免每 token 复制全文；完整不可变 Message 在需要发布快照、权威终态和持久化时物化。涉及 canonical 事件形状时同步更新 loop、extension、CLI 和文档，不能只改 SSE 偷换上游所有权。
- 保留持久化先于可见完成事件的顺序；`runEmitter` 不采用每事件启动 goroutine 的方式“加速”。reader 编码不持有 run 锁；单个慢 reader 不得拖住其它 reader。

**WebUI 先局部减负，再考虑状态拆分：**

- 同一帧只应用一次活动消息的最新状态；`updateRequest` 对未变化的状态不重建整组 records，历史统计和列表投影不随纯正文 delta 全量重算。
- 给活动消息建立稳定身份与局部订阅；若 profile 证明顶层 `setView` 仍占主导，再将 live message 状态从历史视图拆开，而不是先整体更换状态管理方案。
- message_end 将 `entryId` 绑定到已有展示身份，尽量保留行 key、Markdown 段和测量缓存。持久化 id 继续用于去重、分支和分页，不能为了稳定 React key 破坏这些语义。
- 超长纯文本尾部也要按追加片段复用，避免每次重建巨型 text node；不删除可见正文、不缩短回复来达标。

### M3 / P1：增量 Markdown 和平稳定稿

1. 将 `settleBoundaries` 改为持久 scanner：保存源 offset、未结束行、围栏/列表/引用/HTML 状态，只扫描新片段及确实可能改变的语法尾部。归一化同样增量化，并处理 CRLF 跨 chunk 的边界。重写、替换或新消息时明确 reset，不能仅凭字符串长度判断 append。
2. 稳定段在形成时只解析一次，后续显示提交不重新验证全部历史段。定稿不重新挂载已封存正文；只处理尚未确定的尾部与确实失效的块。
3. 4K 限制继续作为活动富文本工作量的保护，但将其度量单位写清楚。单个巨大列表、表格、围栏不能随意切成语义独立的 Markdown 文档；先完整显示源码，再在可控预算下格式化。
4. 跨段 reference definitions、setext 标题、列表连续性、表格列宽、未闭合链接等必须有失效或回退路径。优先使用解析器的块边界/文档级引用信息；不能继续假定空行足以保证最终语义稳定。
5. 普通代码围栏可以在确认有收益后增加追加行快路径；DiagramBlock 继续等待可处理的完整语法。Worker 仅在单块 parse profile 显著时引入，先确认可移出的解析阶段与传输成本；把原字符串送 worker 后又在主线程完整 remark/rehype 一遍没有意义。
6. 语法加工由共享预算队列驱动：活动可见尾部优先，其次可见新稳定块，再是历史/overscan；补充实际耗时反馈，不能只数“每帧一个任务”。格式化期间保留源码视图，避免空白占位导致显示进度倒退。

### M4 / P2：弱网与故障恢复

- 保持同源 `/v1`、SSE 不 gzip、现有注释心跳。评估添加 `X-Accel-Buffering: no` 并验证实际反向代理配置；`Cache-Control: no-cache` 和 Go Flush 本身不能保证中间代理不缓冲。直接端口转发与反代分别测量。
- SSE 单次写/flush 设置可取消、有上限的等待，区分正常高延迟与停止读取。实现时检查包装后的 ResponseWriter 是否支持相应控制，并覆盖 ping 与正文共享写锁的退出路径；不能用很短的全局 HTTP WriteTimeout 杀死长会话。
- 上游增加可配置的读取 idle 检测，区分原始字节活动、有效事件和正文增量；合法 reasoning/长工具阶段不能因为“正文没变化”被误杀。超时先准确标记状态；若重试，遵守现有 attempt/message 边界，不能把重试答案继续拼到旧 partial 或重复执行工具。
- 为网络重连采用有上限、带 jitter 的退避，避免坏链路下 GET/listen 空转；恢复后使用既有 snapshot + cursor，不重复提交 prompt。
- 回放预算需统计工具参数、raw 字段等当前 `eventPayloadBytes` 未充分计入的 payload。128 条/约 4MiB 不是整个 run 的硬内存上限：最新 partial、完成事件、空槽索引等另计，应分别观测。

## 6. 验收：同时约束正确性、持续进度与资源

下列是**拟定门槛，不是已达成结果**。固定机器、浏览器和 fixture 后记录基线；若设备能力不足，应明确调整测试 profile/设计，不能通过减少输出或放弃更新使测试变绿。

| 指标 | 桌面前台目标 | CPU 4×/移动仿真目标 |
| --- | --- | --- |
| 已解码可显示内容→对应 DOM commit，p95 | ≤50ms | ≤120ms |
| 持续输入期间的显示进度最大间隔 | ≤200ms | ≤300ms |
| 恢复前台/突发输入后，本地积压的追赶 | 首个可用渲染周期开始追赶；pendingAge 不连续增长 | 同左；优先源码可见 |
| 普通流单次主线程工作片 | 尽量 ≤8ms，正常样本不出现 >50ms long task | 记录 p95/max，不能通过正文冻结规避 |
| 完成/中止收到后的最终文本可见 | ≤100ms，不清空已有内容 | ≤200ms，格式化可延后 |
| 256KiB 正文编码 | 首阶段相对本次基线显著下降，建议耗时与 B/op 至少降低 50% | 服务端指标；typed append 阶段再验收长度增长时的斜率 |

显示间隔仅在**页面可见、目标消息可见、且有新可显示内容待提交**时统计；模型空档、纯工具事件和用户正在读历史不算文本 stall。首屏完整快照、provider 全文替换、大块最终格式化单列，不能混入稳态追加的分位数掩盖问题。

DOM commit 不等于屏幕已经绘制。测试应在已提交正文上记录对应 revision/语义标记，结合后续 rAF、trace 和必要截图确认可见进度；不能把“已调用 setState”或增长中的内存字符串当作显示完成。Markdown 文本长度会随格式变化，不能只依赖 `textContent.length` 的单调增长。

必须覆盖的场景：

| 类别 | 场景与断言 |
| --- | --- |
| 连续/低频输入 | 5/20/50/200ms delta 间隔；无换行中文长句；首次和最后一次都及时可见，已有 deadline 不被推迟 |
| 网络 burst | 500ms 暂停后集中到达；大 read 内大量事件；显示追赶而非慢放，解码让出 task 后输入和滚动仍响应 |
| 大正文 | 8/64/256KiB 及 1MiB 压力样本；段落、长列表、围栏、表格、跨段引用；正文正确且没有重复/丢失 |
| 历史规模 | 同一流放在 0/400/2000 turn 后；记录 render 次数和 reducer/派生成本；用户读历史时不被拉回底部 |
| 完成与交错 | 无末尾换行、message_end 同批到达、thinking→text→tool、并发工具、中止与错误、provider 重写、切会话/分支 |
| 续传 | 任意 baseSeq 断点、裁剪后重连、无效 patch base、through 与 cursor 同时使用；最终内容与权威 message_end/jsonl 一致 |
| 慢读者 | 一个停止读取、另一个正常读取，1/4/16 reader；正常 reader 进度、CPU/GC 和内存预算分别断言 |
| 浏览器/链路 | Chromium 与 WebKit，桌面/触屏响应式；1.6Mbps 下行、750Kbps 上行、800ms 延迟、CPU 4×，另测实际端口转发/Tailscale |
| 真机与真实模型 | `scripts/run.sh` 使用真实 provider；移动前后台、锁屏恢复、滚动惯性、软键盘；各协议记录上游间隔与客户端增量延迟，避免把模型差异当渲染收益 |

## 7. 交付顺序与变更边界

- [x] **M0**：后端分层抽样、浏览器有界提交指标、固定回放与可见 revision/正文断言、编码基准；数据见第 8 节。
- [x] **M1**：统一 rAF 显示时钟、100ms 兜底与积压追赶、burst 解码 task 让出、终态屏障。
- [x] **M2**：typed encoder、局部 reducer 更新、稳定展示身份；现有收益达标，provider typed change/accumulator 保持按 profile 决定的可选后续。
- [x] **M3**：增量 scanner、跨段引用回退、完整源码占位、渐进定稿；结束与 tail 协调复用 DOM。
- [x] **M4 代码**：可取消写入 deadline、上游 body idle、重连退避、参数回放预算与故障回归。
- [ ] **外部验收**：真实 iPhone/Android 锁屏、惯性、软键盘，以及实际 Tailscale/反代链路；CPU/弱网仿真与 WebKit 不替代这一项。

M1–M3 每个阶段应独立产生可量化收益；可以分别实现，但先有 M0 归因。初期不新增播放缓冲、不迁移 WebSocket、不替换整个 Markdown/虚拟列表库，也不把所有更新集中到更长的节流间隔。

实施时更新现有 [webui.md](../webui.md)、[events.md](../events.md)，涉及 provider/内部事件契约时更新 [provider.md](../provider.md)、[architecture.md](../architecture.md) 和所属包 `doc.go`。修复点添加解释原因的英文注释；流式卡顿已经反复出现，补充相关 postmortem 的遗漏场景与回归证据。

完成代码阶段后运行相应 Go/协议测试、WebUI 完整 fake 矩阵、`test:perf` 和 `responsive.spec.ts`；涉及重连/事件排序时同时维护现有事件与关闭顺序测试。真实 provider 与真机验证单独记账，fake 只用于可重复的测试，不能代替实际服务验证。

## 8. 实施记录（2026-09-27）

### 8.1 最终行为与实现位置

| 层 | 实现 | 关键边界 |
| --- | --- | --- |
| 后端编码 | `internal/loop/wire.go`、`wire_value.go` | typed 字段投影，共享不可变字符串；可变 arguments/details 仍克隆；下界证明 patch 更小时不 marshal 全文；wire 协议与 canonical 事件不变 |
| 传输与回放 | `internal/server/sse.go`、`server.go`、`compress.go` | 30s 单次写/flush，取消解阻塞；心跳结束前取消，归还 writer 前清 deadline，保留正常 HTTP EOF；入队一次计费含工具/raw 字段；SSE 不 gzip，`X-Accel-Buffering: no` |
| 上游活性 | `pkg/llmprotocol/idle.go`、`internal/config` | `streaming.idle_timeout_seconds=300`，0 关闭；只计阻塞 body Read，心跳算活性；partial idle 不自动重试；手动取消同样保留已显示正文 |
| 浏览器接收与显示 | `api/sse.ts`、`lib/stream-batch.ts`、`App.tsx` | 顺序解码后合并；4ms/128 事件让出 task；rAF + 不重置的 100ms deadline；终态即时 flush；GET 失败保留 partial/busy，250ms 起指数退避、jitter、10s 上限，带已应用游标恢复 |
| 消息与 Markdown | `lib/model.ts`、`features/markdown/*`、`features/chat/Chat.tsx` | 局部 no-op reducer；独立 renderKey；增量归一化/scanner，4096 UTF-16 单位活动富文本预算；稳定段与 DOM 跨终态复用；长尾源码文本节点复用；全局引用回退，重写 reset |
| 观测 | `lib/stream-metrics.ts`、`PerfHud.tsx`、loop/server debug 日志 | 浏览器仅显式开启时记录最近 240 次可见提交；后端记录 delta gap / emit / queue / encode / write 与字节；不记录正文 |

解析队列按可见距离、活动尾部和等待时间排序，一次放行一个任务；测量至下一 rAF 的整体回合，超过 32ms 后再让出一帧。这是主线程工作的准入控制，不是能抢占任意 Markdown parser 的硬时间片。不可安全拆分且 >64K UTF-16 单位的单块保留完整源码与手动格式化入口。没有加入人为延迟正文的打字机播放队列。

### 8.2 编码前后对照

同一环境 Intel Xeon Gold 6240R、linux/amd64；`go test ./internal/loop -run '^$' -bench BenchmarkMessageWireEncode -benchmem`。每 128 帧重置连接，后续每帧追加 64B，计入 encoder 和 wire JSON 序列化，含周期性初始快照；不是纯 append 的 O(1) 声明。

| 已有正文 | 调研基线 ns/op | 实现后 ns/op | 基线 B/op | 实现后 B/op |
| --- | ---: | ---: | ---: | ---: |
| 8KiB | 204,567 | 14,939 | 29,417 | 3,461 |
| 64KiB | 1,145,449 | 22,492 | 153,418 | 3,975 |
| 256KiB | 3,061,655 | 40,899 | 556,210 | 5,592 |
| 1MiB | 11,826,938 | 163,706 | 2,153,847 | 11,974 |

256KiB 样本耗时下降约 **98.7%**，分配字节下降约 **99.0%**，分配次数从 62 降到 30/op。长度增长仍有字符串前缀检查和初始快照成本；目前无需为了继续压这个已显著下降的成本，改动所有 provider/extension 的内部增量契约。

### 8.3 浏览器连续输出验收

`cd web && bun run test:perf`：6/6。新 fixture 依次发送 180 次 5ms 增量、150ms 暂停、240 帧集中 burst、20 次 50ms 增量，再发权威终态。每一批都有中文、emoji、加粗和语义标记；rAF 探针同时检查已提交 revision 与对应正文，不能仅更新计数蒙混过关。以下是本次一轮结果，p95 从本地测试源 enqueue 算到观察到 DOM 的下一 rAF，包含解码/排队成本；不包含公网/model 延迟。

| 场景 | p95 可见 ms | 待显示积压最大 ms | 最长帧间隔 ms | 可见提交次数 |
| --- | ---: | ---: | ---: | ---: |
| 桌面 | 33.1 | 45.3 | 66.7 | 97 |
| 已有 400 turn | 31.0 | 67.4 | 33.3 | 97 |
| 390×844、CPU 4× | 33.5 | 206.9 | 116.7 | 100 |

三者都完整显示 440/440 标记、无正文/revision 不一致，完成前后 Markdown 根节点相同。另有浏览器回归验证跨段引用最终解析、结束后的 GET tail 协调仍保留该 DOM，以及恢复连接携带已应用 Last-Event-ID 且不重复 prompt。

### 8.4 验证范围与剩余边界

- WebUI 完整 fake 矩阵：164/164（包括 responsive、流式、停止保留正文、分页/滚动、工具/紧凑历史、重连与新 scanner/SSE 回归）；TypeScript 类型检查通过。
- Firefox 滚动/紧凑历史 11/11；WebKit 滚动 11/11 与 iPhone UA 触控路径 3/3。此环境使用已有 `/tmp/ki-webkit-libs/run-webkit` launcher 补齐系统动态库，未改变产品运行方式。
- 真实模型：`go test -tags live -timeout 5m ./e2e -run Live -v` 通过，实际覆盖 DeepSeek 三种协议、图片/PDF、多图片与 WebUI；没有用 fake 代替真实模型验证。
- `go test ./...`、相关包 `go test -race` 与 `go vet ./...` 通过。SSE 正常 EOF/续传/compact 快照/deadline 测试连续 5 轮通过。800ms 延迟、1.6Mbps 下行/750Kbps 上行、CPU 4× 的 `KI_PERF_WEAK=1` 大历史/大消息测试通过：历史 UI 打开约 1.36s、巨大消息约 1.85s；这是 GET/打开成本，不能用它替代流式正文延迟。编码字段投影/可变 map 基线、工具参数回放预算、慢写 deadline、cancel、idle/心跳都有独立断言。
- Chrome CPU 4×、WebKit iPhone UA 不是 iPhone Safari 真机；本环境不能验证真实锁屏/触摸惯性或用户的 Tailscale/反向代理。代理仍需允许流式转发；body idle 不覆盖等待响应 headers；extension provider 自管其网络活性。
- 没有伪造新文本来填补真实模型或网络空档，也没有把 4096 活动区预算解释为“每条消息只显示 4096 字符”。完整正文始终保留。
