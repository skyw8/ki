# Codex / Ki 工具运行时对齐

日期：2026-10-02。范围：六个 subagent 协作工具、`exec_command`、`write_stdin`，以及它们依赖的输入校验、进程输出、启动接纳和 host 清理。

状态：本轮必要的正确性、资源和效率补丁已实现，包级完整回归与竞态验证通过；不声称两项目架构或所有平台原生行为完全相同。

源码基线（两个工作树开始时均干净）：

- Codex：`/data/hgy/codex`，`444da310e108da16aaeb18fd790b0ac464f08aca`。
- Ki：`/data/hgy/ki`，`ab5cb75900032ad6052fd393b28d9a8b4da75056`。

结论：**核心行为接近 Codex MultiAgentV2，但实现细节并不完全相同。** 上一轮[协作重构](harness-agent-coordination.md)已经实现异步 child、稳定路径、QueueOnly / TriggerTurn 分离、mailbox wait、独立进程 owner 和 root 活跃容量。本次不重复改名，也不引入 Codex v1 的 `send_input` / `resume_agent` / `close_agent`。重点是补齐必要的并发、资源和输出边界，而不是移植 Rust 框架。

## 1. 源码核对

Codex 相对上述 checkout 的入口：

| 范围 | 源码 |
| --- | --- |
| V2 工具、参数与等待 | `codex-rs/core/src/tools/handlers/multi_agents_v2/`、`multi_agents_spec.rs` |
| 身份、启动与清理责任 | `codex-rs/core/src/agent/control/{spawn,spawn_guard,execution,interrupt}.rs` |
| 有效配置快照 | `codex-rs/core/src/agent/child_config.rs` |
| shell 参数与交互 | `codex-rs/core/src/tools/handlers/unified_exec/{exec_command,write_stdin}.rs` |
| 进程 owner 与读写锁 | `codex-rs/core/src/unified_exec/{process_manager,process}.rs` |
| 输出边界与终态顺序 | `codex-rs/core/src/unified_exec/{head_tail_buffer,async_watcher}.rs` |

Ki 对应入口：`internal/tool/builtin/{agent,shell}`、`internal/{agent,process}`、`internal/server/{agent,agent_coordination,server,slash,workspace,runtime_events}.go`。正式工具契约见 [tools.md](../tools.md)，跨包所有权见 [architecture.md](../architecture.md)。

## 2. 已有能力与必须保留的差异

| 项目 | 核对结果 / Ki 决策 |
| --- | --- |
| spawn / identity | 两边 V2 都异步创建 child、使用 `/root/...`、保留身份；Ki 同时返回内部 agent/session ID |
| 普通消息 / follow-up | QueueOnly 不启动 idle；显式 follow-up 启动工作。Ki busy follow-up 明确排入下一代次，不能把 Codex 活跃输入边界实现当成完全相同 |
| wait / completion | wait 只等调用者 mailbox/steer，不消费正文；完成结果发结构 parent。Ki 保留 durable generation ledger，而非退化成 Codex best-effort completion |
| 执行容量 | root child 默认 4，等待 turn 仍占用、无固定深度上限；Ki 容量满时接受持久 follow-up 排队，不复制 Codex 容量错误 / residency eviction |
| context fork | all/none/N 继承完整已完成 turn、tool 配对和附件；none 仍继承运行配置。Ki 排除当前 parent turn |
| shell 等待 | yield 只是观察时间；同 terminal 串行、不同 terminal 并行；取消观察 / child turn 不杀 owned process |
| 进程容量 | Ki live cap 64、retained cap 256；不复制 Codex LRU 可能淘汰 live process 的软上限策略 |
| 输出模型 | Codex 保留 head+tail；Ki 保留可续读尾部游标和完整原始日志。借鉴有界队列效率，不改变 Ki 输出协议 |
| PTY 收尾 | Ki Unix root 退出后最多等 200ms drain，强制截断明确诊断；Windows ConPTY 的正常 EOF 可能依赖 terminal.Close，不将该正常路径误报为截断。Codex 可选 ReleasePseudoConsole 属于其原生 backend，不修改 Go 依赖内部来伪装相同 |
| 有效配置 | Ki 继承 parent provider/model/thinking/cwd，busy 时不允许修改模型。全局工具开关在 child 同样生效；extension `activeTools` 是 session-scoped 下一 occupy 选择，不是安全权限，也不自动继承到 child |
| 额外架构 | Codex sandbox/审批/远端 exec、role/model 覆盖、runtime residency、durable sleep、message board 没有 Ki 对等契约；本次不伪造或机械引入这些能力 |

Codex handler 的 serde 数值类型会阻止整数溢出；其 V2 协作调用默认不并行，而 Ki 工具批次默认并行。因此 Ki 必须自行保证初始接纳、同名 reservation、interrupt 和 teardown 顺序，不能照抄 handler 而省掉同步。

## 3. 本轮缺口与改进方案（已实施）

以下“原状”均来自上述 Ki 基线，不是声称 Codex 在每个平台或失败路径都没有问题。

| 优先级 | 原状 / 风险 | 对齐方案 |
| --- | --- | --- |
| P0 | 初始 task 在 durable generation 接纳之前已经进入 registry；并发 stop/follow-up 可以看到半初始化身份 | 初始 map publication、容量接纳和首条持久输入在同一 controller 事务中完成，失败不发布 |
| P0 | generation 完成后丢失下一代次容量竞争，pending work 仍可呈 Completed，interrupt 误以为无工作；仅清 pending 还可能认领上一代完成结果 | queued identity 保持 Pending/waiting_resource；Stop 清除 pending，即使上一代已终态；仅中断 active generation 才抑制其 completion |
| P0 | 成功 runner 丢弃 cancel function；Close 每个 run 单独等 2s，后一个 Close 可提前返回 | 每代次释放自己的 context；共享关闭完成屏障，观察期限只限制调用方等待，不提前释放仍有 writer 的资源 |
| P0 | server 只等待 child 后就收集 manager，root setup/release 仍可能创建 manager 或写进度；最新 run registry 还可遮住旧代次 release writer | 统一关闭 admission，fence 后开始 manager stop/close；per-session/global writer 屏障覆盖 root/child/warmup/dispatch 所有代次，最后才删共享文件/spool；关闭后的 manager 请求只能得到已关闭对象 |
| P0 | tree abort 的 root release 可在清理期间 dispatch queued replacement，绕过只有 agent 的临时 fence | 临时 session occupy/dispatch fence 与 agent fence 配合；所有旧 writer 和 stop 结束后解除，再 dispatch 保留队列，不永久删除身份 |
| P0 | 删除先遍历 tree 再 abort；遍历后仍可 spawn，release 后仍可消费队列重新 occupy | 删除前设 session tombstone 与 agent subtree fence，等待已接纳 spawn rollback/commit 后再遍历；writer / release drain 后才删目录 |
| P0 | 交互排队与取消争用时返回空快照，或取消后仍写 stdin；尚未进入交互的取消可能消费输出 | 检查取消再执行副作用；排队/启动前取消返回不消费输出的有效 snapshot；已进入观察返回的增量仍按既有规则推进游标 |
| P0 | finish 可丢 pending / UTF-8 flush delta；spool 错误若停止 pipe drain 会影响外部命令 | final delta 明确由 finalizer 发布；继续 drain 并单独保留 I/O 错误；完成屏障包含最后 listener 回调 |
| P0 | 已退出进程仍可触发 raw process-group kill；PTY dup descriptor 未设置 close-on-exec | 退出状态拒绝重复 signal；停止请求幂等；PTY fd 禁止传给后续 exec，避免错误信号和资源继承 |
| P1 | 每次溢出 append 都复制 1MiB tail，pending/UI preview 有类似复制成本 | 有界环形缓冲；写入只复制新 chunk，观察时按预算抽取；保持 UTF-8 起点、游标与截断标记 |
| P1 | completed handle 留作续读历史，即使输出全被消费仍持有最多 1MiB buffer；Codex 退出后移除 entry | Ki 保留 handle，但输出消费完就释放 retention window；独立 16KiB preview/cursor 不变，未读数据不丢 |
| P1 | 多进程 shutdown/TerminateAll 每个进程顺序等待，耗时可乘 live 数量 | 批量发 stop，再共享等待预算；并发 Close 等同一完成屏障 |
| P1 | 数值 schema bounds 未执行；float/int narrowing、duration 乘法或输出预算可能溢出；required null 被接受 | 共享检查整数转换，mandatory null 拒绝、optional null 仍为默认；工具各自校验范围，再做 duration/handle 转换 |
| P1 | direct task ID 每次扫描整个 registry；fork all/none 已 trim，但数值分支未 trim | direct ID 用 map O(1) 访问；fork 字符串先 trim 再规范化；不增加第二套身份索引 |
| P1 | output watcher 在读写路径同步调用 listener，慢持久化会阻塞外部命令；Interact 每次 write 都发 progress | 有界异步最新快照合并、100ms 节流和 10000 次中间更新上限；初始/终态及 final callback 屏障保留，原始日志/增量游标不受预览省略影响 |
| P1 | tty shell 继承 pager/color 环境，git diff/help 可停在不可见的 pager 提示 | 子进程局部禁用默认 pager/color；空 pager 不要求 Windows 安装 cat；保留 locale、代理、PATH 和显式交互命令 |

实现保持既有模型参数和 jsonl/SSE/GET 通道；不增加通用 job REST 路由、不恢复重启前 OS handle，也不把进程事件塞入 mailbox 触发模型。

进程树控制也不等于 sandbox：Unix 显式脱离原 shell 生命周期的 daemon/后代可能在 root 退出后存活，不能靠对已退出 PID/PGID 再发信号假装可靠回收。若要改变此契约，需要独立的 native backend/稳定 descendant ownership 设计，不在本轮偷偷增加自动杀后台 daemon 的行为。Windows 验证为交叉编译，不冒充执行过 ConPTY 回归。

## 4. 验收方法

- 逻辑竞态用 channel / gate / `testing/synctest`，不靠固定 sleep。
- 覆盖初始接纳失败、同名 reservation、queued generation、cancel function 释放、多个关闭观察者与期限；快照不可出现半初始化身份。
- 覆盖 canceled stdin waiter 无副作用、未读输出保留、短尾和不完整 UTF-8 flush、spool short-write / failure 后仍 drain、退出后 stop、PTY fd 和跨平台编译。
- server 测试保留“occupy 已完成、runPrompt 尚未调度”的 root setup 窗口，以及“done 已通知、release 尚未完成”的窗口；不只测试 child runner。
- 删除测试验证 tombstone 在遍历之前建立、已接纳 spawn 能收敛、队列不重启被删 root，flat fork / sibling / 其它 root 不被级联。
- 计时使用 `-count=1`，有界缓冲和 ID lookup 用 microbenchmark；不把整个回归墙钟波动当作性能提升。
- 最终重新 typecheck/build `web/dist` 后执行 `go test -tags embed -count=1 ./...`，该入口包含 Bun / Chromium，不重复单独跑浏览器。

## 5. 验证记录

初始基线：

- agent/tool：0.39s wall（包耗时 0.032s / 0.007s）。
- server/loop/session：8.91s wall（包耗时 7.871s / 0.231s / 2.646s）。
- 共享参数校验补丁：`go test -count=1 ./internal/tool ./internal/tool/builtin/internal/support`，0.44s wall，通过。
- `cd web && bun run typecheck && bun run build`，通过；构建仅有既有大 chunk 提示。

Agent 最终定向验证：

- `go test -count=1 ./internal/agent ./internal/tool/builtin/agent`：0.032s / 0.008s，包耗时与基线基本相同；fresh compile wall 0.82s，不能与缓存命中的初始 wall 直接作性能结论。
- `go test -race -count=20 -timeout=60s ./internal/agent ./internal/tool/builtin/agent`：通过，4.73s wall。
- direct task ID lookup benchmark（100ms）：1 个身份 100.5ns → 150.8ns，100 个 2525ns → 243.8ns，10000 个 227271ns → 227ns；优化消除 registry-size 扫描成本，不能推广为端到端模型加速。
- 实际在补丁前失败的回归：run context 未取消、空白数字 fork、越界 wait、容量竞争后 pending 呈 Completed、pending-only Stop 误认领上一代结果。atomic publication / terminal callback tail 为源码推导的竞态，新 gate 测试通过，但未在 pristine 工作树单独运行；不伪称所有新测试都做过前后失败对照。

Shell 最终定向验证：

- process / shell 基线 0.101s / 1.774s（2.28s wall）；最终 warm 0.160s / 1.801s（2.39s wall）。新增阻塞观察契约有必要的 25ms 观察成本，其它新增逻辑测试仍很轻；不缩短断言窗口换速度。
- 定向 `-race`：通过，4.47s wall；两个包的 `windows/amd64`、`darwin/arm64` 编译通过，原生执行只验证 Linux。
- 满 1MiB retention、32KiB write microbenchmark：旧 clone 模式 1.389–1.682ms/op、约 2.37MiB/op、2 allocations；ring 0.967–1.150µs/op、0B/op、0 allocations。只衡量 retention copying，不宣称命令吞吐提高同样倍数。

Server 与最终集成：

- 增加的 lifecycle cases 使用 gate/channel；server 初始完整 suite 7.871s，补丁后定向完整 suite 8.152s，无固定 sleep 的新增清理测试不引入秒级观察成本。首次 terminal-listener 测试错误地用 synctest 等待 mutex，测试自身挂起；改为真实 child Run 包装后的 channel-parked completion callback，terminal mutex tail 则由 agent-local TryLock 测试覆盖，没有新增生产测试 hook、降低断言或删掉覆盖。
- 首次 `go test -tags embed -count=1 ./...` 因上述测试 harness 问题在 76.43s 中断；此前 `ki/e2e` 已完整通过（48.888s，包含 Bun + Chromium）。当时仅修正测试、没有改生产代码，因此用 `go list -tags embed ./... | rg -v '^ki/e2e$'` 得到其它所有包，执行 `go test -tags embed -count=1 -timeout=2m $packages` 全部通过（10.66s wall，server 7.636s），复用有效 browser 结果而非立即重复跑。
- 最终审查又补齐 completed/consumed retention window 释放（保留 preview/cursor），新增“已退出后消费”和“消费后退出”两种回归。此生产输入变化后，重新执行完整 `go test -tags embed -count=1 -timeout=5m ./...`，**全部通过，50.55s wall**（e2e 46.943s、server 7.424s、process 0.182s、shell 1.808s）。不是将前一次中断命令标成成功，也不复用旧二进制来验证最终补丁。
- 最终 Playwright 报告 `web/test-results/e2e-parallel/run-AfEjUc`：171 units，180 expected、0 unexpected、0 skipped、0 flaky。浏览器都来自 Go 完整回归入口，没有另跑 `bun test:e2e`。
- runtime 竞态：`go test -race -count=1 -timeout=2m ./internal/agent ./internal/process ./internal/server ./internal/loop ./internal/session ./internal/tool/...` 全部通过，32.09s wall。最终替换的 `TestDeleteDrainsAgentCompletionCallback` 另行定向 `-race` 通过（1.129s package）；retention 最后补丁的 process/shell `-race` 再次通过（4.84s wall）。
- 最终补丁后再次交叉编译 process/shell 测试二进制，`windows/amd64`、`darwin/arm64` 均通过；临时编译产物已清理。
- `git diff --check`、新增文档相对链接和 Markdown code fence 检查通过。

本轮没有真实 provider、live model、原生 Windows/macOS 或浏览器 perf 验证；不把 fake scripted regression、交叉编译或 microbenchmark 当成这些验证。
