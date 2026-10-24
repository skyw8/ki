# Ki 对照 Codex 的 Harness 效率调研

调研日期：2026-10-24。状态：源码调研完成，待选择实施项；本文不代表功能已实现。

对照范围：本地 `/data/hgy/ki`（`e7175d40c2cd3b39a4be0aebc90223543c9021da`）与 `/data/hgy/codex`（`970111f6cb62cc02fe36a82ea7d71b37a2889c97`）。两者调研起点工作区均无改动。Ki 链接相对本仓库，Codex 链接指向同级 checkout；行号对应上述版本。

方法：沿真实调用链对照 agent 调度、模型循环、工具 dispatcher、Code Mode、MCP 发现、终端输出与 session 持久化，结合现有测试确认能力边界。本次未改运行代码、未重启服务、未运行负载基准或故障注入。下文区分**确定的实现事实**、**有条件的风险**与**需要测量的收益**，不宣称已经获得性能提升。

## 1. 结论与优先级

Ki 已有比较完整的 harness。主要机会集中在三个方向：

1. **统一跨层契约**：合法 MCP 目录可能让 Code Mode 整体失败；大 schema 无法通过正常搜索披露；终端完整日志绕过普通输出预算；中断路径可能静默丢失持久化失败。
2. **减少关键路径上的重复工作**：工具声明与 BM25 重建、fork 逐条 fsync、读取小页却全量加载大文件、反复启动 login shell。
3. **改善调度与可观测性**：工具与后续模型流重叠、direct/nested 共用并发策略、subagent ready queue 公平性、树级成本控制，以及可取消观察的完整清理。

优先级口径：P0 表示存在明确条件可导致核心能力整体不可用，应首先修正；P1 表示明确的可靠性问题或主要效率机会；P2 表示需基准或产品契约支持的优化。P0 不表示本次发现了线上事故。成本为单项工程量粗估，包含有针对性的测试与文档更新，存在交叉，不能直接相加为项目排期。

| 编号 | 优先级 | 建议 | 证据/预期价值 | 粗估成本 |
|---|---|---|---|---|
| H01 | P0 | 解耦目录、调用次数与 schema 展示预算 | 合法 MCP 配置可阻断所有 `exec` | 2–4 人日 |
| H02 | P1 | 为大 schema 提供可完成的披露路径 | 当前约 10KiB 以上完整声明可被永久 omitted | 2–4 人日 |
| H03 | P1 | 缓存 spec、JS 声明和搜索索引，减少双份披露 | 确定重复序列化、索引构建与 prompt 重复 | 3–5 人日 |
| H04 | P1 | 单个 tool item 完成后允许开始执行 | 可与后续模型流重叠，收益依模型输出形态 | 5–10 人日，高风险 |
| H05 | P1 | 文本 `read` 改为有界流式分页 | 当前小页仍全量读取、转换、拆行 | 2–4 人日 |
| H06 | P1 | 给终端完整 spool 建立独立且真实的资源预算 | 普通 8MiB/256MiB 配额不覆盖终端文件 | 2–4 人日 |
| H07 | P1 | agent 取消/收尾持久化失败可见且可恢复 | 写失败后可能在重启时恢复本应丢弃的 pending | 2–4 人日，跨文件一致性另计 |
| H08 | P1 | direct/nested 共用有界并发准入策略 | 已有并行，缺工具/服务器级共享策略 | 3–6 人日 |
| H09 | P2 | subtree interrupt 复用 owned cleanup + ctx 观察 | 保留完整 drain，同时避免调用方无限等待 | 1–2 人日 |
| H10 | P2 | 每 root ready queue，按 turn 公平轮转 | 当前全量扫描、按 ID 排序，存在抢回容量风险 | 2–4 人日 |
| H11 | P2 | 未发布 fork 批量持久化并支持取消 | N 条历史至少 N 次 events fsync | 2–4 人日 |
| H12 | P2 | 继承历史来源与句柄语义更明确 | 减少父身份/任务/旧 handle 混淆，效果需验证 | 提示 0.5–1 人日；结构化 2–4 人日 |
| H13 | P2 | 统一模型可见上下文估算 | 文本启发式漏参数/媒体，portable 序列化又可能高估 | 2–4 人日 |
| H14 | P2 | 公开有效终端预算，增加可选头尾摘要 | 更快看到末尾错误，同时保留完整日志恢复 | 1–3 人日 |
| H15 | P2 | 按 profile 实测决定 shell snapshot | 避免重复 login 初始化，收益高度依环境 | 3–6 人日 |
| H16 | P2 | 有限重试透传服务端等待建议 | 当前本地指数退避无法遵守 Retry-After | 2–3 人日 |
| H17 | P2，可选 | 树级成本预算和提醒 | 并发上限不能限制累计模型费用 | 基础 3–5 人日；精确恢复另计 |
| H18 | P2，可选 | spawn 指定 model/effort，后续再加 role | 允许按任务难度选模型，需同时验证质量 | model/effort 2–4 人日 |

H05、H06、H10 等包含 Ki 自身的优化推导，不代表已证明 Codex 对应实现更优。具体借鉴点见各节。

## 2. 已有能力与应保留的优势

| 领域 | Ki 已有能力与证据 | 对本次调研的约束 |
|---|---|---|
| 子代理身份与调度 | [identity.go](../../internal/agent/identity.go#L19) 的具名路径/root 隔离；[tasks.go:176](../../internal/agent/tasks.go#L176) 原子 per-root capacity；`:455` 持久 followup；`:609` 代际完成认领 | 不新增一套 agent registry、持久队列或完成通知体系 |
| 邮箱、wait 与恢复 | [agent_coordination.go:76](../../internal/server/agent_coordination.go#L76) 先持久再送 live inbox；[mailbox.go](../../internal/loop/mailbox.go#L41) channel 唤醒；[agent.go:126](../../internal/server/agent.go#L126) 重启恢复 | 不将“没有邮箱/无轮询等待/恢复”列为差距 |
| 中断与生命周期 | [tasks.go:769](../../internal/agent/tasks.go#L769) StopSubtree；[identity.go:176](../../internal/agent/identity.go#L176) admission fence；[tasks.go:852](../../internal/agent/tasks.go#L852) CloseContext 在观察取消后继续 owned cleanup | 改善失败报告、等待接口，保留 fence 与清理责任 |
| 终端 | 真正 PTY/ConPTY、跨观察继续执行、增量 write_stdin、单 handle 交互串行化、64 live process、1MiB 内存环与完整输出文件，见 [shell_process.go](../../internal/process/shell_process.go#L112) | 不能建议“增加后台命令/PTY/输出分页”作为新能力 |
| 输出恢复 | [exec_command_test.go:189](../../internal/tool/builtin/shell/exec_command_test.go#L189) 已验证完整头尾、Read 分页、write_stdin 游标；[stream_test.go:12](../../internal/process/stream_test.go#L12) 验证 raw spool 无损 | Ki 完整文件恢复比单纯头尾摘要保留更多信息，应继续保留 |
| 文件变更 | [edit.go](../../internal/tool/builtin/file/edit.go#L112) 批量替换；[mutation_queue.go](../../internal/tool/builtin/file/mutation_queue.go#L77) 规范化后的多路径锁；[apply_patch.go](../../internal/tool/builtin/file/apply_patch.go#L114) freeform patch 与预检；[Codex 风格测试](../../internal/tool/builtin/file/apply_patch_codex_test.go#L18) | 不重做 apply_patch；独立路径可并行，不应被全局写锁退化 |
| 搜索 | [engine.go](../../internal/search/engine.go#L95) 流式消费 rg JSON，有分页下推、超时与早停 | 不再列“增加流式 grep”为优化 |
| Code Mode | session worker 懒启动、每 exec 新 VM、固定能力快照、共享校验/hooks/审计、取消后 join；[api.go](../../internal/codemode/api.go#L36) 已有 8 cells、32 pending/cell、512 calls/cell 等边界 | 缺少共享调度不等于完全没有并发限制 |
| 模型传输与压缩 | Codex OAuth 已有 WebSocket 增量请求、cache key、错误恢复，见 [websocket.go](../../extensions/codex-oauth/websocket.go#L291)、[model.go](../../extensions/codex-oauth/model.go#L386)；host 已有 preflight/threshold/overflow 压缩 | 不把 WebSocket、prompt cache 或自动 compaction 当成全新待建设能力 |

另外，Ki [ForkRecentHistoryAt](../../internal/session/session.go#L1609) 真正支持最近 N 个完整已完成 turn；当前 Codex [v2 spawn:232](../../../codex/codex-rs/core/src/tools/handlers/multi_agents_v2/spawn.rs#L232) 把正整数 `fork_turns` 映射为 FullHistory。这一点 Ki 更完整，应保留并继续测试。

## 3. 工具目录、Code Mode 与并发

### H01：目录预算冲突可使无关 exec 整体失败

**确定事实。** Ki MCP 每 server 最多 4096 tools，单输入 schema 最多 256KiB，目录还有 16MiB 总预算，见 [manager.go](../../internal/mcp/manager.go#L215)、[tool.go:41](../../internal/mcp/tool.go#L41)。但 Code Mode [validateRequest](../../internal/codemode/api.go#L174) 用 `MaxToolCalls=512` 限制 `len(req.Tools)`，又要求每条 definition 的 Description 不超过 64KiB。

实际链路：

1. [server.go:2558](../../internal/server/server.go#L2558) 的 `nestedTools` 包含全部允许的 MCP 工具。
2. [definitionsWithDeferred](../../internal/tool/builtin/code/tools.go#L66) 将完整 schema 拼进 Description，先加入 `defs`，之后才根据 Deferred 跳过外层模型描述。
3. [ExecuteRaw:145](../../internal/tool/builtin/code/tools.go#L145) 把所有 defs 放入 `req.Tools`。
4. [remoteSession.execute](../../internal/codemode/manager.go#L209) 在执行前调用 validateRequest。

因此，总嵌套目录超过 512，或任意工具的描述加 schema 超过 64KiB，即使代码只是 `text(1)` 也会失败。Deferred 不规避这条链。请求另有 transport 总字节限制，单纯提高 512 仍不构成完整修复。

**建议。** 分开定义目录数量、每 cell 实际调用数、工具元数据、schema 展示与 IPC frame 预算。worker 只持有调用所需元数据，完整 schema 由 host 的不可变 registry 保存并校验；目录过大时采用注册/引用或分批传输，避免每个 exec 重发完整目录。局部声明无法展示时应明确降级，不能让无关工具一起失效。

**验收。** 合法的 513/4096 工具目录，以及 64/128/256KiB schema，均不阻断简单 exec；最大实际调用次数仍独立生效；transport 超预算给出准确错误；direct 与 nested 的执行权限和参数校验一致。

### H02：大 schema 不能通过常规搜索完成披露

**确定事实。** [catalog.go:298](../../internal/tool/discovery/catalog.go#L298) 给完整声明的桶为 `12KiB−2048`。单个序列化工具声明超过约 10KiB，就始终进入 `omitted_tools`，也不会加入 `discovered_tools` 激活列表。缩小 query 或 limit 无法让单条声明变小。这与 H01 的 64KiB 门槛是两个独立问题。

执行权限仍在，JS 的 ALL_TOOLS 也有元数据；这里缺的是常规发现路径的完成能力，不能描述成工具完全不能调用。

**借鉴。** Codex [json_schema_types.rs](../../../codex/codex-rs/code-mode-protocol/src/json_schema_types.rs#L6) 对声明渲染大小、引用展开次数分别限额，超限可退回 `unknown`；[schema compaction](../../../codex/codex-rs/tools/src/json_schema/compaction.rs#L15) 对模型侧 schema 做有损压缩。这本身不证明 Codex 提供完整定义的后续查询路径。Ki 可提供紧凑签名加按工具名获取完整定义的分页/分段工具能力，复用现有工具发现流程；完整校验 schema 必须保留，不能把压缩定义冒充完整 schema。无需为此增加 REST 路由。

**验收。** 10–256KiB 合法 schema 有明确且可走完的获取路径；每页有界且能确定结束；披露状态只根据真正交付的内容激活；禁用工具不能经历史披露恢复权限。

### H03：缓存不可变声明与索引，减少重复披露

**确定事实。** [server.go:2571](../../internal/server/server.go#L2571) 每次 occupy 调 `discovery.New` 重建 BM25；[modelTools:2634](../../internal/server/server.go#L2634) 每轮请求调用 `promptSet.Build()`，反复遍历与 marshal schema。MCP adapter 的 [Parameters](../../internal/mcp/tool.go#L68) 本身还通过 marshal/unmarshal 防御性复制。搜索后，工具既进入直接 provider tools，又被写进 exec 描述。

**借鉴。** Codex [tool_search.rs:66](../../../codex/codex-rs/core/src/tools/handlers/tool_search.rs#L66) 用不可变源引用身份、动态 metadata 相等性及 source listing 复用索引；[mcp.rs:325](../../../codex/codex-rs/core/src/tools/handlers/mcp.rs#L325) 共享 immutable spec，并用 OnceLock 缓存匹配预算/渲染参数的声明。[registry.rs:389](../../../codex/codex-rs/core/src/tools/registry.rs#L389) 在 model_spec override 时禁止误用原缓存。

**建议。** 以 Ki 已有不可变目录快照为边界缓存 spec、序列化字节、JS 声明和搜索索引；把模型可见集合当成轻量投影。披露后可让一个入口提供完整 schema，另一入口提供紧凑引用，但 direct/nested 同时可用的设计应保留。缓存键覆盖目录 revision、active tools、provider profile、展示预算和 hook override，工具变化时失效，缓存有总容量。

**验收。** 100/1000/4096 工具目录下比较冷启动与重复轮次的 CPU、alloc/op、prompt 字节和 p95。同一未变目录不反复建索引、编码全目录；开关、list_changed、reload 引起的目录变化在下一次 occupy 创建快照时正确失效，当前 occupy 保持原能力快照；hook/model 声明 override 按实际生效范围重建投影。输入变短不应以模型更频繁查 schema、增加总轮数为代价，需测任务总 token 与完成率。

### H04：工具执行与后续模型流重叠

**确定差异。** Ki [loop.go:384](../../internal/loop/loop.go#L384) 等 `streamWithRetry` 整体结束，到 [:443/:452](../../internal/loop/loop.go#L443) 才提取和执行工具。Code Mode 内部并行不能消除外层这一等待。

Codex [handle_output_item_done](../../../codex/codex-rs/core/src/stream_events_utils.rs#L316) 在单个 tool item 完成后先持久化，再调用 runtime；[parallel.rs:215](../../../codex/codex-rs/core/src/tools/parallel.rs#L215) 即刻 spawn 执行任务。存在 `defer_tool_dispatch` 分支，不是所有路径都会提前执行。

**建议。** 扩展 provider-neutral delta，明确区分参数增量与完整 tool item；仅完整且通过校验的调用可进入 scheduler。先选择具备明确 item-complete 语义的 adapter 和只读工具试点，再决定跨三个协议扩展。不能只见 JSON 暂时可解析就执行。

**改造边界。** 必须定义完整调用持久化、批次中断、length/aborted、stream 失败与重试、post-model hook、tool call/result 配对、结果顺序。已执行副作用不能因 stream 重试被再次执行；当前“length 时拒绝整批工具”的语义也需要明确调整。保留需要 defer 的 hook/工具路径，沿用 jsonl/SSE，不另建结果通道。

**验收。** 用可控流发送早完成工具与后续慢 item，测“参数完成→execution_start”、stream/tool overlap ms、端到端 p50/p95。验证部分参数从不执行，取消有配对结果，流失败不会重复副作用，hook 行为不漂移。收益取决于后续流长度与工具耗时，不预设加速比例。

### H08：direct 与 nested 共用有界并发准入

**确定事实。** Ki [executeBatch](../../internal/loop/dispatcher.go#L362) 按 `cfg.Parallel` 决定整批并行，服务端默认开启；[DispatchNested](../../internal/loop/dispatcher.go#L49) 直接进入 execute，JS runtime 为回调启动 goroutine。现有 cell/pending 限制有效，文件路径锁也有效，但没有统一的工具/服务器级并发策略。

Codex [parallel.rs:220](../../../codex/codex-rs/core/src/tools/parallel.rs#L220) 允许并行的调用取读锁，其他取写锁；[MCP handler](../../../codex/codex-rs/core/src/tools/handlers/mcp.rs#L175) 依据配置或 read_only_hint 决定并行；[Code Mode delegate](../../../codex/codex-rs/core/src/tools/code_mode/mod.rs#L409) 与 direct 共用调用入口。

**建议。** 在 Ki 共享 dispatcher 上增加可取消准入，支持 session/root 上限、MCP server 上限及显式独占策略。远端 hint 可作为默认参考，允许配置覆盖。保留细粒度文件锁，不用全局写锁串行化所有文件变更。外层 exec 是编排容器，不能持有阻止自身 nested calls 的独占 gate 或唯一 permit，否则会自锁。

**验收。** direct/nested 混合调用同一服务器时上限一致；只读调用重叠、独占调用符合契约；排队取消及时；饱和状态下 exec 调 nested 不死锁。分别报告排队和执行耗时，不能把等待隐藏在工具时间里，也不能预设限流必然更快。

## 4. 文件读取与进程输出

### H05：小页 read 不应全量加载大文件

**确定事实。** [read.go:47](../../internal/tool/builtin/file/read.go#L47) 使用 os.ReadFile；[:138](../../internal/tool/builtin/file/read.go#L138) 先读完文件，再转换字符串、[:182](../../internal/tool/builtin/file/read.go#L182) Split 全文件，到 offset/limit 才选页。即使只读 20 行，也付出全文件 IO 与内存。大 spool 的分页恢复会重复放大这一成本。[output.go](../../internal/tool/builtin/file/output.go#L9) 的逐步 Join 截断也应一起测量。

**建议。** 先探测有界文件头区分文本/媒体；文本路径流式定位并只保留所需页，设置单行/单页字节边界与取消检查。精确 total_lines 若要求扫到 EOF，也应保持有界内存；若允许返回未知总行数，需要同步调整契约。反复读取日志尾部可再增加带文件 identity/size/mtime 的稀疏行索引，正确处理 append、truncate、replace；没有索引时按行 offset 读尾部仍需扫描前缀，不能承诺常数时间。

此 Codex checkout 的 core handlers 没有一套与 Ki 对等的原生 Read/Grep/Edit，不能声称“Codex Read 已经流式”。这是从 Ki 已有工具使用模式得到的热点优化。

**验收。** 10/100/1000MiB 文本首/中/尾页，记录分配、RSS、耗时和实际读入字节；首小页内存不随文件大小增长。覆盖超长单行、UTF-8 边界、CRLF、空文件、并发 append/truncate、取消。现有图片、PDF、notebook 与 spool 不再 spill 的行为继续成立。

### H06：终端 spool 配额闭环

**确定事实。** 普通输出 [spill](../../internal/tool/output/store.go#L226) 才裁到 8MiB/文件，并经 [reserve](../../internal/tool/output/store.go#L248) 记入 256MiB/session 预算。终端调用 [CreateOutputFile](../../internal/tool/output/store.go#L185)，只创建裸 *os.File；[shell_process.go:178](../../internal/process/shell_process.go#L178) 获取后在 [:296](../../internal/process/shell_process.go#L296) 直接 Write，未计费。Normalize 看见 output_file 后 [skip](../../internal/tool/output/store.go#L270)，所以不会补记预算。

这是资源策略缺口，也正是当前能够完整保存长日志的原因，不能写成输出丢失。64 live process 与 1MiB 环不限制这些文件累计磁盘占用。

**建议。** 用可计费 spool writer 替换裸文件接口，明确终端专属单文件/session/服务总预算；不要直接套普通文本的 8MiB 上限。达到上限后的保留、丢弃和恢复能力必须显式定义，例如保留头尾并报告 incomplete/omitted bytes；停止保存后仍需持续 drain 子进程 stdout，避免因文件配额造成管道阻塞。写入失败也必须反映到快照，不能仍承诺“完整输出”。

**验收。** 持续大输出与多进程竞争下，磁盘占用和账目有界；达到预算/磁盘写失败时错误可见，子进程不因未 drain 挂死；完整日志在预算内无损，预算外的遗漏明确；删除/关闭后释放账目。沿用现有恢复测试，不能用缩短日志掩盖问题。

### H14：返回预算透明化，增加头尾摘要

**确定差异。** [observe](../../internal/process/shell_process.go#L463) 将 max_output_tokens 静默 cap 到 10000，按约 4 bytes/token 返回 cursor 后下一段。尚未被消费的输出会继续分页；环中已经丢弃的部分可从完整文件恢复。

Codex [process.rs:45](../../../codex/codex-rs/core/src/unified_exec/process.rs#L45) 的 pending 和 transcript **各有一个** HeadTailBuffer<1MiB>；[buffer 实现](../../../codex/codex-rs/utils/head-tail-buffer/src/lib.rs#L17) 保存头尾并记录中间遗漏。其 [工具上下文](../../../codex/codex-rs/core/src/tools/context.rs#L526) 还将调用预算与模型策略取较小值。该链路不是 Ki 式完整日志文件。

**建议。** 返回 requested/effective budget、截断原因与剩余/遗漏信息。可为终态/超长输出增加头尾摘要，让编译失败末尾信息首轮可见；保持现有 cursor 分页契约，不能用摘要隐式推进游标导致消费者跳过内容。

**验收。** 首次结果包含尾部错误的比例、恢复完整信息需要的调用数、模型实际收到字节数。区分“本次预览省略”“内存环淘汰”“完整文件未保存”；保持现有分页恢复测试。

### H15：shell snapshot 先测量再实施

Ki [shells.go:40](../../internal/process/shells.go#L40) 默认每次以 -lc 启动，login=false 为 -c。Codex [features:1152](../../../codex/codex-rs/features/src/lib.rs#L1152) 的 v1 shell_snapshot 为 Stable 且默认启用；[runtimes/mod.rs:286](../../../codex/codex-rs/core/src/tools/runtimes/mod.rs#L286) 在适用时重放快照并使用 -c。

Codex V2 snapshot 默认关闭，见 [features:1174](../../../codex/codex-rs/features/src/lib.rs#L1174)，预热还有环境条件。不能把 V2 的所有缓存/预热收益算作默认行为。

**建议与验收。** 先在空 profile、常见工具链 profile、昂贵 profile 测冷/热启动和 profile 执行次数。若开销显著，再缓存环境/函数快照，并定义 shell 路径、cwd、profile、环境和 reload 失效。每次调用仍是独立进程，快照不意味着持久 shell。覆盖 linux/amd64、darwin/arm64、windows/amd64 的适用与回退语义，禁止把 Bash snapshot 假定为 PowerShell/ConPTY 的统一方案。

## 5. Subagent 调度、持久化与继承

### H07：中断成功不能掩盖持久化失败

**确定事实。** [stopTask:815](../../internal/agent/tasks.go#L815) 清空内存 pending 后忽略 persistLocked 返回值；[closeOwned:882](../../internal/agent/tasks.go#L882) 及部分完成/认领路径也忽略错误。

可行触发：followup 已经持久化 → interrupt 内存清空 pending，但状态文件写失败 → API 仍成功 → 下一次成功落盘前崩溃 → [restoreAgentTasks](../../internal/server/agent.go#L126) 从旧 metadata 恢复并 ResumePending。这里的风险是**已存在持久队列的取消未可靠提交**。本次尚未故障注入；完成认领失败是否产生重复交付还受消息幂等键保护，不能直接断言必然重复。

Codex [AgentTreeTeardownGuard](../../../codex/codex-rs/core/src/agent/control/runtime.rs#L129) 记录阶段、对象与失败，guard 未完成也会留下报告，可借鉴故障可见性。

**建议。** 汇总状态持久化错误，区分“运行已停止”和“取消状态已持久化”；可靠禁止恢复需可重试提交或 tombstone。继续按 Ki 的 internal/state 读写版本化状态，不绕过它。收尾失败不能阻止其他目标被取消和 drain，不能提前释放清理责任。

**验收。** 对 pending、取消、完成认领分别注入写失败并重启；不得静默报告强取消成功后恢复执行。明确部分失败契约，重复中断/恢复幂等；业务交付与完成 claim 的跨文件一致性单独验证。

### H09：StopSubtree 的观察应支持 ctx

[InterruptAgent](../../internal/server/agent_coordination.go#L56) 只在入口检查 ctx，随后 [stopSubtree](../../internal/agent/tasks.go#L777) 无条件等待 runDone 和 drained。若 runner/工具/rollback 迟迟不返回，调用方取消也无法停止等待。

Ki **已经有** [CloseContext](../../internal/agent/tasks.go#L852) 的“观察取消，后台继续完整清理”模式。建议复用到 subtree stop：后台 owner 持有 fence，多个调用方观察同一个 barrier 和结果；ctx 只中止等待，不提前解除 fence。

**验收。** 挂住 runner 后取消 observer 能及时返回，子树仍拒绝新工作；释放 runner 后清理完成才解除 fence；多 waiter 结果一致，无资源泄漏。Codex teardown guard 也无法终止永不返回的 runner，不能把它描述成强制结束所有工作。

### H10：ready queue 公平性与规模性

[resumeReady:581](../../internal/agent/tasks.go#L581) 扫描 Controller 全部 tasks，逐个锁定后筛 root/ready，再按 task ID 排序。[executeRun:362](../../internal/agent/tasks.go#L362) 释放 permit 后立即 startNext，同任务可先于异步 resumeReady 再占满容量。

扫描成本与非 FIFO 是确定事实；等待饥饿是存在可行调度的风险。例：并发 1，A 持续 pending，B 已等待，A 每次完成均可能抢回 permit。不能据此宣称 Codex 已提供更公平队列，当前证据只支持 Ki 自身改进。

**建议。** 每 root 维护去重 ready queue，接受顺序决定 turn 排序；每执行一轮重排队尾，permit 释放统一交给 scheduler。队列索引与持久 pending 分开，重启可重建，保留原子容量与 admission fence。

**验收。** 确定性测试 A 持续 followup 不越过更早 ready 的 B；root 上限不变、跨 root 不互阻。100/1k/10k 已结束身份加少量 ready 的基准记录 dispatch p95、扫描数、锁等待与 CPU，目标不随无关历史任务线性增长。

### H11：fork 写放大与启动取消

[newAgentChild](../../internal/server/agent.go#L84) 同步准备历史。[session.go:1404](../../internal/session/session.go#L1404) 对 entries 逐条 appendLocked，经过 [:1123](../../internal/session/session.go#L1123)、[:1186](../../internal/session/session.go#L1186) 到 [appendRawAndConfigLocked](../../internal/session/session.go#L1251)，每条 f.Sync 并原子更新 config。因此 N 条 fork 至少 N 次 events fsync，不只是推测。附件只复制引用集合，不能说复制全部附件。

**建议。** 对尚未发布的新 child 使用批量构建：写完整 JSONL、生成最终 config，完成必要 sync 后再发布身份和启动。支持 ctx，失败沿现有 cleanupChild 回滚。先做批量提交，再决定是否需要共享不可变历史存储；后者会扩大 session 删除、附件与分支语义的改造面。

**验收。** 100/1k/10k entries，0/100MB 引用附件，对比 spawn p50/p95、events fsync 次数和写入字节。目标将历史 events fsync 从 O(N) 降到 O(1)，同时保留 leaf、compaction、header、cache key、附件引用和崩溃恢复一致性。取消中途不留下已发布孤儿身份。

**重要限定。** Codex [config:1413](../../../codex/codex-rs/core/src/config/mod.rs#L1413) 的 preserve_fork_prefix 默认 true，[spawn:1267](../../../codex/codex-rs/core/src/agent/control/spawn.rs#L1267) 默认可保留全部 ResponseItem；还受上下文基线是否可保留等条件影响。不能宣称 Codex 默认 fork 历史更短、必然省 token，或已证明它没有落盘成本。

### H12：继承历史来源与旧 handle 语义

Codex [spawn:1180](../../../codex/codex-rs/core/src/agent/control/spawn.rs#L1180) 标记 inherited_user_message、清理 sender/user order；[:1288](../../../codex/codex-rs/core/src/agent/control/spawn.rs#L1288) 清理父 compaction 授权归属；[:1340](../../../codex/codex-rs/core/src/agent/control/spawn.rs#L1340) 明确父身份、任务、动作批准属于祖先，历史工具调用不代表继承 live session/process handle。

Ki [agent.go:69](../../internal/server/agent.go#L69) 已告知 child 新身份、任务和提问边界。建议先补 inherited context/handle 的准确提示，再按需要增加结构化 provenance；不要引入当前任务无需求的完整授权审查体系，也不能据此宣称当前 Ki 有权限漏洞。

**验收。** 定向场景：父使用过 shell handle、有多个历史任务、出现旧身份与批准语句，child 仍执行本次任务、正确向 parent 回报，不误用父 handle。测错误尝试数和完成质量，行为收益需要模型实验；确保提示放在稳定前缀之后，不破坏已有缓存亲和。

### H17/H18：树级预算与子任务模型选择

Ki 已有 usage/progress 统计，但 [Agents config](../../internal/config/config.go#L29) 主要控制 MaxConcurrent；[spawn schema](../../internal/tool/builtin/agent/tool.go#L46) 只包含 task_name/message/fork_turns。并发控制不能限制无限 followup 的累计成本。

Codex [rollout_budget.rs:48](../../../codex/codex-rs/core/src/rollout_budget.rs#L48) 在 root tree 共享预算，按输出、非缓存输入权重或 provider units 记账，并按阈值提醒；[child_config.rs](../../../codex/codex-rs/core/src/agent/child_config.rs#L57) 处理 model/effort/role 优先级与校验。

**建议。** 可选实现 root 共享成本预算与提醒，复用已有 usage；明确达到上限时停止新采样还是停树。先提供显式 model/effort override，复用 provider catalog 校验并持久化到 child config，role 预设后续再做。默认继承继续保留。

**验收。** 多 child 统计不丢计/重计，缓存输入计费明确，提醒去重，恢复行为确定；已在途请求可能超支，不能承诺硬预算零超支。模型选择同时比较费用、延迟与任务完成率，不以更便宜但结果更差作为效率提升；错误配置在创建 child 前拒绝，followup/重启保留选择。

## 6. 上下文估算、重试与测量

### H13：按模型可见内容统一估算

[compact.go:45–105](../../internal/compact/compact.go#L45) 优先使用最近 usage，这是已有正确方向；但没有可用 usage 时的 char4，以及 usage 后 trailingTokens，主要只计 Message.Text。它不包含 tool 参数、图片等非文本内容。另一方面 Portable 分支按完整 JSON 序列化字节估算，可能把 base64、metadata 等传输体积计为 token，并产生整段序列化分配。

Codex [estimate_item_token_count](../../../codex/codex-rs/core/src/context_manager/history.rs#L1073) 按模型可见 item 内容估算，分别处理参数、工具结果、媒体与 opaque 内容，并排除部分传输 metadata。它仍是启发式，不是精确 tokenizer。

**建议。** Ki 统一 typed estimator，纳入 tool arguments/freeform、媒体、system/schema 的相应预算；usage 可用时只估新增尾部，失效时按实际模型上下文恢复估算。opaque checkpoint 的估算策略应独立，不能把加密字节直接当普通文本。不要复制特定 Codex 模型的图片常数到所有 provider。

**验收。** 大参数、图片/PDF、多工具声明、checkpoint 展开、无 usage 与旧 usage 等场景，比较估算和实际 input usage、overflow/提前压缩次数；同内容改变非模型 metadata 不应显著改变估算。压缩正确性和模型实际请求内容以既有 provider 测试验证。

### H16：尊重服务端 retry 建议并保留安全边界

Ki [streamWithRetry](../../internal/loop/loop.go#L627) 有有限指数退避、ctx 取消、overflow 特殊处理与 NonRetryable 分类。普通 HTTP [postStream](../../pkg/llmprotocol/http.go#L260) 和 OAuth [upstreamFailure](../../extensions/codex-oauth/http.go#L103) 没有把 Retry-After 作为结构化信息传给 loop。

Codex [responses_retry.rs](../../../codex/codex-rs/core/src/responses_retry.rs#L64) 统一 retry state、服务端 deadline 与 transport fallback，不因 fallback 绕过服务器等待要求。

**建议。** 通过 provider-neutral error metadata 传递 HTTP seconds/date 或协议 retry advice，结合有限重试预算和可取消等待；同步记录重试原因、实际 delay、attempt。保留已经收到不安全部分响应时的 NonRetryable 保护，不自动重放可能已经执行的副作用；不要照搬可选的无限网络重连模式。

**验收。** 429/503 advice、无 advice、非法值、取消等待、预算耗尽、overflow、部分流失败；明确是否允许截断极端 advice 或直接结束，不能过早重试又宣称遵从服务端。故障恢复时间和重复请求数一起测量。

### 测量基础：扩展现有 telemetry，不新建统计体系

Ki 已有 [telemetry.go](../../internal/telemetry/telemetry.go#L36) 的 model request/cache/tool/run 统计、session trace 与 agent 进度。优先补充或验证以下指标可查询：

- 工具：item complete、queue enter、execution start/end、准入等待、stream overlap。
- 目录：revision、索引构建/命中次数、schema 编码字节、direct/exec 声明字节。
- 子代理：ready wait、spawn/fork 耗时、首模型请求时间、每 turn permit 使用与队列顺序。
- 输出：raw/preview/persisted/omitted bytes、实际磁盘记账、Read 扫描/保留字节。
- 收尾：取消请求→运行停止、barrier 完成、持久化错误和阶段报告。

不记录 prompt、schema 原文、文件内容和 credential。新增观测不应在高频 delta 上增加同步重活。当前 [modelTelemetry](../../internal/loop/loop.go#L843) 每次请求重新 hash 全 history；只有 profiling 证明这部分显著后，再考虑不可变消息摘要缓存，hook 改写历史时正确失效。

## 7. 不建议直接移植的方案

- **整体换成 Rust/V8/gRPC。** 当前证据指向契约与调度热点，没有基准证明语言/JS 引擎迁移收益。先修 Ki 的目录、缓存与 IO。
- **把 Codex 的 max_heap_size 字段视作现成硬堆隔离。** 本地 [service.rs](../../../codex/codex-rs/code-mode-runtime/src/service.rs#L42) 主动置 None，[runtime/mod.rs:187](../../../codex/codex-rs/code-mode-runtime/src/runtime/mod.rs#L187) 使用默认 V8 CreateParams；协议字段不等于实现兑现。
- **用头尾缓存替换 Ki 完整日志。** Codex bounded buffer 有价值，但不能因此抹掉 Ki 已有恢复能力；应分别设计预览与持久预算。
- **直接复制进程 LRU 淘汰。** Codex [process_manager.rs](../../../codex/codex-rs/core/src/unified_exec/process_manager.rs#L1768) 必要时会终止旧活进程；Ki 达 live 上限时拒绝新建。自动杀旧进程改变用户契约，不能仅作为性能优化引入。
- **默认裁掉 fork 全部工具历史。** 双方都有缓存/上下文连续性的考虑，Codex preserve-prefix 默认保留；Ki recent N turns 已更完整。
- **所有写工具全局独占。** Ki 现有路径锁支持独立文件并行；共享调度应尊重这个能力。
- **照搬 Codex resident thread eviction。** 其常驻 CodexThread 与 Ki 每轮 runner/session open-close 不同。Ki 也已有有界 session cache；应先测 tasks/deliveries/worker 的实际常驻成本。

## 8. 实施顺序与验证方案

建议按依赖逐步推进，每项单独保持可评审、可回退：

1. **先修能力边界。** H01、H02、H06、H07；增加目录边界、输出记账与持久化故障注入。它们不以性能收益为成立前提。
2. **减少重复成本。** H03、H05、H11；先记录固定数据集的基线，再比较改造后的分配、IO 和耗时。
3. **统一调度。** H08、H09、H10，随后 H04。提前执行依赖明确的准入、取消、持久化和失败语义，不宜先孤立抢跑。
4. **按实测拓展。** H12–H18 结合真实 profile、多模型任务质量和成本数据选择，不自动全部实施。

基准建议：

| 工作负载 | 固定输入 | 主要观测 |
|---|---|---|
| 目录与发现 | 100/1000/4096 tools；1/16/64/256KiB schema，组合不超过上游目录总预算 | 冷/热构建、alloc/op、schema 字节、失败条件、总模型轮数 |
| 模型流与工具 | 可控 tool-item 边界、慢后续流、慢工具、取消/错误/hook | 参数完成到执行延迟、overlap、整轮 p50/p95、重复执行数 |
| 子代理 | 多 root；并发 1/4；持续 followup；100/1k/10k 历史身份/entries | 排队公平、spawn 首请求、fsync、跨 root 干扰、恢复正确性 |
| 文件与终端 | 10/100/1000MiB 文本；多进程持续输出与配额边界 | RSS、alloc、磁盘峰值、Read 扫描量、完整恢复/遗漏标记 |
| 真实模型任务 | 相同仓库任务、provider/model/effort、同一资源快照 | 成功率、质量、费用、总 token、总耗时、缓存命中 |

先用 Go/Bun 单测和可控 adapter/process fixture 验证 host 语义；fake 仅用于自动化测试。真实模型质量与端到端验证使用独立实例/隔离 KI_HOME，保持当前运行服务不变。按相同环境记录变更前后 wall time；一次有效结果可复用，避免重复跑重测试。

本次仅新增此调研文档；实施时同步更新已有 [tools.md](../tools.md)、[code-mode-design.md](../code-mode-design.md)、[mcp.md](../mcp.md)、[architecture.md](../architecture.md)、[session.md](../session.md) 或 [provider.md](../provider.md) 中对应契约。修复已确认缺陷时补 postmortem，区分已观察事故与静态发现风险，不把本次尚未复现的场景写成线上事实。
