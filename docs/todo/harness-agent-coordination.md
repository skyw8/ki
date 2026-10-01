# Harness 协作与长任务调度改进方案

日期：2026-10-01。状态：协作与进程交互重构已实现，最终回归通过。

Ki 基线：`b5efe630ce7620303c7b255619cce03208b84b4e`。

范围：统一 shell 执行与后台交互、agent 消息、续跑、等待、进度、统计与共享执行资源。排除模型能力、推理档位、provider 延迟/额度，以及已修复的结果双渠道交付、超时丢 snapshot、工具轮阈值压缩。正式运行契约已同步到对应 `docs/*.md` 与 package `doc.go`；下面保留调研依据，并以本节实施决策为准。

## 当前实施决策

- 工具面一次切换：read/write/edit/grep/glob/apply_patch；exec_command/write_stdin；六个 agent 协作工具。注册只发布 snake_case schema，调用兼容 PascalCase 与 extension 原始名；SendMessage 是新 send_message 的大小写别名，旧 to/summary 参数移除。
- 两个独立 owner：ShellProcessManager 保存 OS 进程，AgentController 保存逻辑 agent。删除 JobStore、compositeTaskStore、Agent foreground promotion、TaskOutput/TaskStop 与固定深度上限。
- spawn 异步，fork_turns 默认 all；具名路径稳定，普通消息 QueueOnly，follow-up 是显式新任务。busy follow-up 进入下一代次，不取消当前工作。每 root 默认 4 个活跃 child，完成后身份保留、容量释放。
- Unix PTY / Windows ConPTY；exec 观察预算 250–30000ms，write_stdin 空输入 5000–300000ms、非空输入 250–30000ms。pipe 拒绝普通输入，Ctrl-C 为中断请求。取消观察不杀进程，强制停止由现有 abort 的 process/tree scope 执行。
- toggles v2 保守迁移旧开关，agent.json v3 保留路径/pending/ledger。全部身份注册后才恢复 pending；不恢复 OS handle，也不自动重跑重启前中断的当前任务。
- 事件归约 generation 统计与结果；progress 有 revision、phase、lastActivityAt、currentTools、waitingFor、runStats 和 lifetimeStats。等待执行容量与 mailbox 可见。旧 metadata 的 lifetimeStatsComplete=false，避免伪造完整累计值。
- 进度经 sideband JSONL、既有 SSE/session GET/WebUI；不移动 model transcript leaf。CLI inspect/trace 支持 last-known runtime，工具/消息等待时间按区间并集计算，未归因时间保留 unknown。重连不允许较旧 generation/revision 覆盖新状态。
- M4 的资源租约/验证复用仍为条件方案，不在本轮范围；不引入 residency 池、durable sleep、Agent Teams 或 provider/model 覆盖。

实施与验收记录在文件末尾；调研 checklist 中超出这些明确决策的可选增强继续作为后续研究，不作为协作工具切换的阻塞条件。

## 1. 建议与顺序

先把协作控制从自然语言往返中取出来：**消息可以送达而不启动工作，等待可以被关键输入唤醒，进度由执行事件生成，统计绑定实际 generation。** 保留一个 Go runtime、现有 session tree、工具注册和事件通道。

| 阶段 | 优先级 | 改动 | 预期收益 |
| --- | --- | --- | --- |
| M0 | P0 | generation 范围统计与结果提取；协作耗时诊断 | 先修正当前确定的统计缺陷，建立可验证基线 |
| S0 | P1 | ShellProcessManager、exec_command/write_stdin；替换 Bash/PowerShell 与旧 task 工具依赖 | 统一后台进程交互和明确的运行所有权 |
| M1 | P1 | 六个 agent 协作工具、异步 spawn、稳定身份与执行容量 | 减少普通沟通造成的续跑；建立可复用 child 生命周期 |
| M2 | P1 | mailbox/steer wait_agent，复用任务通知与输入队列 | 任一有效协作输入即可唤醒，不再选定单个 child 阻塞 |
| M3 | P1 | 事件驱动进度、任务快照和按修订查询 | 减少“进度如何”的问答及 transcript 扫描 |
| M4 | P2，条件实施 | 显式 shell 资源租约、验证记录复用 | 减少人工测试槽位协调和重复重型检查 |

本轮以 Codex v2 为主要实现参考，详见 3.1 的源码核对与工具映射。M0 → S0 → M1 → M2 → M3 为内部实施顺序，最终一次切换完整模型工具面。M4 要在测得资源等待/重复验证成本后决定；不先建通用工作流引擎、Agent Teams、自动任务 DAG 或全局文件所有权系统。

### 1.1 目标工具与重构边界

目标是替换旧的共享 task 工具契约：**删除模型可见的 Bash、PowerShell、Agent、SendMessage、TaskOutput、TaskStop，新增 exec_command/write_stdin 和六个 agent 协作工具**。移除旧执行入口/schema；新工具均接受 snake_case 与 PascalCase alias，不并存两套 agent 生命周期。历史 transcript 保留原 tool name；历史显示不要求旧工具仍可执行。

| 执行域 | 模型可见工具 | 运行语义 |
| --- | --- | --- |
| shell 启动 | `exec_command` | 使用统一 ShellProcessManager；由 shell 解析层选择平台解释器，初次等待到期返回 live process handle，进程继续运行 |
| shell 继续交互 | `write_stdin` | 对同一 process handle 收集后续输出、写 stdin、请求 Ctrl-C；取得 exit code。不是 agent 消息接口 |
| subagent | `spawn_agent`、`send_message`、`followup_task`、`wait_agent`、`interrupt_agent`、`list_agents` | 采用协作工具命名与核心行为；默认 snake_case，接受对应 PascalCase 别名 |
| host 控制 | typed process inspect/terminate 与 agent/session/tree shutdown | 服务 server 生命周期及 UI 的明确停止动作；不新增 TaskStop 模型工具 |

exec_command 首版采用 Codex 的核心参数：cmd 必填，workdir、shell、login、tty、yield_time_ms、max_output_tokens 可选。write_stdin 使用 session_id、chars、yield_time_ms、max_output_tokens。移除 Bash/PowerShell 工具及 command/timeout/run_in_background 旧 schema；新名称支持对应 PascalCase alias。yield_time_ms 只控制本次观察；host 若配置真实运行期限，独立计时并明确返回期限终止原因，不把它混进 yield。首版不新增模型可见的 run_timeout_ms、environment_id 或审批参数。结果包含 terminal session_id、running/exited、增量输出与 exit code，不返回旧 task_id。PTY/stdin 必须提供实际交互能力。

shell 是解释器选择，不是工具分派：省略时沿用当前会话已解析的默认解释器；显式路径经 shell resolver 判断类型并构造参数。Unix 与 Windows 均只暴露 exec_command。workdir 相对路径基于当前调用的有效 cwd 解析；每次 exec 不隐式继承上一次命令中的 cd/Set-Location。login 在 Unix 映射 login/non-login flags，在 PowerShell 映射 profile 行为，不能把 -lc 原样传给 PowerShell。模型提示标明默认 shell、edition 和支持能力；PTY 模式与 pipe 模式分别构造交互参数，保留正确 native exit code、UTF-8、PATH/代理及 extension 环境注入。

`write_stdin` 的整数 session_id 是 terminal handle，`spawn_agent` 的 target 是 canonical task path；host 内部还保留 Ki session ID、agent ID 与 run generation。类型和 owner 均区分，不允许混用或靠字符串前缀猜类别。

运行时拆为两个所有者：

- **ShellProcessManager**：启动、PTY/pipe、stdin、输出 drain、exit、signal/kill、process capacity 和 shutdown。它由当前 JobStore 重构，不新建一个包裹旧 foreground/background 分支的 facade。
- **AgentController**：logical agent registry、spawn/fork、消息投递、turn admission、generation、completion、interrupt 与恢复。它整合当前 AgentStore 和 server agent orchestration，不继续实现 composite TaskStore 的 shell/agent Get/Wait/Stop 混合接口。

已有 process-tree termination、OutputSpool、session tree、state 迁移、completion identity、input gate、SSE/loop.Event 和 telemetry 继续复用。删除旧模型工具不等于删除这些已验证能力。旧 TaskOutput 的“主动读取与通知争抢结果”不再作为新主路径；通知接受仍需按 identity 幂等，只有用于旧双入口仲裁的代码才删。

一次完成模型工具面切换，内部按阶段/commit 开发。最终切换同时更新 Set.Build/Catalog、tool toggles、extension 内建工具可见性/执行桥、prompt/async note、typed tool results、WebUI 和测试。旧 tool toggles 若改变含义，应经 internal/state 做显式版本迁移，保守保留禁用意图；不得以新增名称或 shell 参数绕过已有禁用设置。源代码中的旧名字仅可留在迁移、历史投影或必要的回归 fixture，不留隐藏旧执行入口。

## 2. Session 证据与因果边界

样本：`01a0f05ff8ed7ce1876ff4d5b95bd46a`。用 CLI `session show --view detailed --full --tool-args --json`、`session inspect --json`、`session trace --all --json` 读取主会话及 12 个 child；按主会话创建时间 `2026-09-30T03:33:36Z` 排除 flat fork 继承的早期历史。

观察到主 agent 发出 90 次 `SendMessage`，返回 65 次 `steered`、25 次 `resumed`；主会话收到 166 条 agent 来源 user message，其中 39 条为完成通知。展示层 child `01a0f16a7d9d7ccea88e1f58874d10a6` 的历史跨约 149 分钟，包含 308 个 assistant 步骤、60 次 `SendMessage`；主 agent 的 53 次 `TaskOutput` 累计等待约 46 分钟。

这些数字是改进入口，**不等于 25 次续跑都无效、166 条消息都可丢弃，或能节省 46 分钟墙钟时间**。工具会并行，child 跨度包含续跑和间隔；要靠新诊断区分追加实现、请求决策、状态汇报和重复读取。原始 session 横跨约 14 小时，包含输入间隔、额度错误后的停顿和后续手动压缩，不能当成单次持续执行。

当前仍存在的代码事实：

| 事实 | Ki 实现锚点 | 影响 |
| --- | --- | --- |
| 所有 agent 消息都可导致执行；终态 child 自动续跑 | `SendAgentMessage` / `messageAgent` / `queueSessionMessage`：[agent.go](../../internal/server/agent.go)；`QueueOrResume`：[agent_tasks.go](../../internal/agent/tasks.go) | 普通沟通与追加工作没有运行语义上的区别 |
| `summary` 从工具解析出来，但 server 路由只传 message | `internal/tools/send_message.go`（原实现，已移除）、[agent.go](../../internal/server/agent.go) | 没有可复用的协作消息预览与结构化收据 |
| Inbox 只有 Push/Take/Has；工具批次完成后才 drain | [loop.go](../../internal/loop/loop.go) | 单目标阻塞等待期间，用户 steer 和其它消息只能积压 |
| TaskOutput 只有单个 task_id，只等待终态或超时/取消 | `internal/tools/task_tools.go`（原实现，已移除） | 主 agent 需要选定一个任务等待，等待期间不能及时接入其它完成通知/用户 steer |
| task 的工具数/token 在 startRun 清零，在 executeRun 结束时填入 | [agent_tasks.go](../../internal/agent/tasks.go)、`internal/tools/jobs.go`（原实现，已移除） | 运行中没有足够的 agent 进度快照 |
| runChildAgent 每次扫描整个 MessagesToLeaf，累计所有 assistant 用量并选最后非空正文 | [agent.go](../../internal/server/agent.go) | 续跑统计混入旧 generation；本次无正文时可能拾取旧结果，需定向测试确认 |
| 显式后台 Agent 返回 Terminate=true，所有调用均 terminate 时结束 parent | `internal/tools/agent_tool.go`（原实现，已移除）、[loop.go](../../internal/loop/loop.go) | 启动后台 child 与 parent 是否继续工作耦合 |

已有基础也必须复用：`CompletionIdentity{taskId,generation}` 与 `ClaimResult`/`CommitNotification`、稳定 clientRequestId、human/system 队列优先级、版本化 agent metadata、context-only 持久队列、统一输出溢出存储、文件 mutation queue、OTLP tool diagnostics。不要再建立并行的结果仲裁或消息落盘系统。

## 3. 参考实现与取舍

### 3.1 Codex v2：实施主参考

本地 `/data/hgy/codex`。本节重新核对于 2026-10-01，源码基线 **`444da310e108da16aaeb18fd790b0ac464f08aca`**，工作树干净；此前方案参考过 `d2b254fd17ced848dbc269705373cf5a02ed3284`，以下工具和运行细节以本次快照为准。这里描述本地代码，不保证某个已发布客户端具有相同配置或工具展示。

Ki 的实施目标是 **异步 agent、消息/任务分离、mailbox wait、可续用身份**。上游 v1 仅用于解释差异，不把它的目标状态等待混入 Ki 的 mailbox 等待契约。M1 不再采用单个 `SendMessage.mode`；M2 不再优先扩展多目标 TaskOutput。

#### 3.1.1 工具清单与实际返回

v2 的核心协作面是以下 **6 个工具**。默认 namespace 为 `collaboration`；provider 不支持 namespace 时可以注册为普通函数。定义在 [multi_agents_spec.rs](/data/hgy/codex/codex-rs/core/src/tools/handlers/multi_agents_spec.rs)，注册在 [spec_plan.rs](/data/hgy/codex/codex-rs/core/src/tools/spec_plan.rs)，执行入口在 [multi_agents_v2.rs](/data/hgy/codex/codex-rs/core/src/tools/handlers/multi_agents_v2.rs)。

| 工具 | 参数 | 行为和返回 |
| --- | --- | --- |
| `spawn_agent` | 必填 `task_name`、`message`；可选 `fork_turns`、`agent_type`、`model`、`reasoning_effort`，后 3 项是否展示受配置影响 | 创建 child，提交初始任务后立即返回，不等待工作完成。默认返回 `{task_name:"/root/name"}`；关闭 metadata 隐藏后增加 `nickname`。内部仍保留 thread ID，工具结果不直接返回 ID、最终正文或完整历史 |
| `send_message` | `target`、`message` | `QueueOnly` 投递。idle 时不启动新工作；active 时在允许的输入边界消费。handler 成功输出为空，不能把它当执行完成收据 |
| `followup_task` | `target`、`message` | `TriggerTurn` 投递。idle 时开始新的 turn；busy 时在消息边界或当前工具结束后接入。拒绝 root target；成功输出同样为空 |
| `wait_agent` | 可选 `timeout_ms` | 订阅调用者 input queue 的 mailbox/steer 活动。实际结果为 `{message, timed_out}`；没有 targets、status map、agent 正文，也不返回已更新 agent 名称列表 |
| `interrupt_agent` | `target` | 中断目标当前 turn，返回 `{previous_status}`。保留 agent 身份用于后续消息和任务；拒绝 root、自身；已卸载 runtime 的已知 agent 不会为中断而重新加载 |
| `list_agents` | 可选 `path_prefix`，无尾随 `/` | 返回 `{agents:[{agent_name,agent_status}]}`，按 canonical path 排序；过滤可相对调用者解析。列表包含已加载 root/child，也可包含已完成但仍驻留的 child；不是所有持久 agent 的历史目录，已卸载 child 会被略过 |

状态表示需要按源码读取：字符串 `pending_init/running/interrupted/shutdown/not_found`，以及携带正文/错误的 `completed/errored` 对象。Ki 可以使用自己的 typed status 与有界预览，不要求复制这套枚举序列化。`list_agents` 也不是逐 token 进度工具。

`wait_agent` 的描述提到更新摘要，但 [wait.rs](/data/hgy/codex/codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs) 实际只返回 `Wait completed.` / `Wait interrupted by new input.` / `Wait timed out.`。实现时以 handler 为准，不能按描述推导出它有多目标结果读取能力。

工具层并发也要单独看：[ToolExecutor](/data/hgy/codex/codex-rs/tools/src/tool_executor.rs) 的 `supports_parallel_tool_calls` 默认 false，这 6 个 v2 handler 没有覆盖；[multi_agent_tool.rs](/data/hgy/codex/codex-rs/core/src/tools/multi_agent_tool.rs) 只转发这个能力。**child 异步运行不等于协作控制调用可任意并发执行**。Ki 现有 loop 会并行执行工具批次，因此名称、admission、消息 input gate 和 interrupt 必须自己保证原子次序，不能照抄 Codex handler 后省掉锁。v2 参数 structs 使用 deny_unknown_fields，旧 items/interrupt/fork_context 等字段会被拒绝，不静默忽略。

v1 的 `send_input`、`resume_agent`、`close_agent` 与 targets 型 `wait_agent` 不属于上述 v2 面。v2 把续跑放进 `followup_task` 和按需加载，把终止当前工作放进 `interrupt_agent`；本轮不实现 v1 兼容层。

#### 3.1.2 启用条件与默认值

[config/mod.rs](/data/hgy/codex/codex-rs/core/src/config/mod.rs) 的版本选择先看 `Feature::MultiAgentV2` override，再看模型声明和其它 feature；v2 feature 可强制选择 v2。工具注册还有 root/child 能力判断：root 可获得 v2 工具，已有 agent path 的 child 需要 model metadata 声明 v2。这里只记录源码 gate，Ki 不新增模型/provider 适配工作。

| 本地默认配置 | 值/影响 |
| --- | --- |
| `max_concurrent_threads_per_session` | `4`；下文区分执行容量和驻留容量，不能解释成“最多创建 4 个持久 agent” |
| wait min/default/max | `10_000 / 30_000 / 3_600_000` ms；高于 max 返回参数错误，低于 min 向上 clamp 并在结果说明 |
| `tool_namespace` | `collaboration` |
| `wait_agent_enabled` | `true`；可以关闭 wait 工具 |
| `disable_direct_message` | `false`；设 true 隐藏 `send_message` 和 `followup_task` |
| `hide_spawn_agent_metadata` | `true`；默认仅返回 canonical task name |
| `expose_spawn_agent_model_overrides` | `true`；只是 schema 暴露开关，不要求 Ki 本轮做模型覆盖 |
| `non_code_mode_only` | `true`；工具曝光还受 code mode 配置影响 |

v2 还明确忽略 v1 的 configured agent_max_depth，源码测试 `multi_agent_v2_spawn_agent_ignores_configured_max_depth` 覆盖这一行为；不要把 v1 默认深度 1 当作 v2 限制。Ki 的本轮实现同样移除固定深度限制，改为按 root 限制活跃 child turn，默认 4；root 不计数，wait_agent 中的 child 仍计数。

还有 role、root/subagent usage hints、subagent developer instructions、message board 配置。它们不构成额外 6 个协作工具；Ki 第一版只实现直接通信，不引入 message board、远端 agent backend 或工具动态搜索体系。

#### 3.1.3 身份、命名和上下文 fork

[agent_path.rs](/data/hgy/codex/codex-rs/protocol/src/agent_path.rs)、[target.rs](/data/hgy/codex/codex-rs/core/src/agent/control/target.rs)、[registry.rs](/data/hgy/codex/codex-rs/core/src/agent/registry.rs) 将模型可用的名称和内部 thread ID 分开：

- root 为 `/root`；root 创建 `research` 得 `/root/research`，该 child 创建 `verify` 得 `/root/research/verify`。名字只允许小写 ASCII、数字、下划线；`root`、`.`、`..`、空串和 `/` 不能作为 task-name segment。
- 相对 target **相对发送者**解析。`/root/a` 发给兄弟 b 要用 `/root/b`，不能写 `b`；给 parent 用 canonical parent path。工具统一先 resolve 到 thread ID，后续控制按 ID 执行。task path 是逻辑路径，不是 host 文件路径，不能用 `filepath.Join` 改其 `/` 语义。
- spawn 为名称/slot 建立 reservation，初始任务接受后 commit，失败时 Drop 释放。并发创建同名 child 要在 registry 判断，不能靠模型避免重复名。已完成 agent 可继续用原名字追加工作，不应再 spawn 同名“续跑”。
- [spawn.rs](/data/hgy/codex/codex-rs/core/src/tools/handlers/multi_agents_v2/spawn.rs) 中 `fork_turns` 默认 `all`；`none` 是不继承对话历史，正整数字符串如 `"3"` 是最近 N 个 turn。大小写 `none/all` 被接受，空值按默认；`"0"`/非法值报错。旧 `fork_context` 会明确拒绝。
- `none` 不等于没有 system/runtime 配置；它仍有 child 基础指令、环境和初始任务。fork 也不是启动一个共享 parent 可变 history 的指针。底层 [control/spawn.rs](/data/hgy/codex/codex-rs/core/src/agent/control/spawn.rs) 从 rollout 构建 child 上下文，处理 turn 边界、参考上下文与继承的协作活动；Ki 实现 N-turn 选择时必须保留 tool-call/result 配对，不能按最后 N 条 message 切片。
- [child_config.rs](/data/hgy/codex/codex-rs/core/src/agent/child_config.rs) 从调用 step 的有效配置构建 child，刷新 cwd、permission/approval、模型设置和 developer instructions，并在有要求时叠加 role。v2 可以显式选择 role，即使继承 full history；不要照搬 v1 full-fork 的 role 拒绝规则。本轮只借鉴有效配置快照与 role 恢复边界，不扩大模型选择范围。

#### 3.1.4 投递、等待与完成通知的真实关系

[message_tool.rs](/data/hgy/codex/codex-rs/core/src/tools/handlers/multi_agents_v2/message_tool.rs) 共用解析/resolve/dispatch 路径，拒绝空白 message；[delivery.rs](/data/hgy/codex/codex-rs/core/src/agent/control/delivery.rs) 将 sender、recipient 与模式形成 `InterAgentCommunication`。普通正文渲染为 `Message` 或 `NewTask`；另有 encrypted 内容路径，Ki 不必为此添加消息加密实现。

[input queue](/data/hgy/codex/codex-rs/core/src/session/input_queue.rs) 同时持有 pending mailbox 和当前 turn 输入，控制 `CurrentTurn/NextTurn` 投递阶段。接收方没有 active turn 时，普通 message 保存在 mailbox；只有 `trigger_turn` 输入进入共享 pending-work 启动器。active turn 开始会 drain 先前邮件；结束边界可能把后来邮件留到下一次工作。因此“发送立即成功”不是“对方已经读到”。

`wait_agent` 先订阅 watch receiver，再检查 pending input/mailbox，避免通知落在“检查后、睡眠前”的窗口。已有 pending 会立即结束等待；后来的 mailbox 或 user steer 都可唤醒。watch 是活动信号而非正文存储，多次活动可以合并；正文仍按队列交付。返回 wait 的工具结果后，正常 loop 将 pending 输入交给模型，不由 wait handler 再复制一次消息。

[completion.rs](/data/hgy/codex/codex-rs/core/src/agent/control/completion.rs) 捕获 terminal outcome：完成正文/错误消息路由给 **结构 parent**，标记 `trigger_turn=false`；成功完成的 UI activity 可另发给启动该 turn 的 agent。兄弟发 follow-up 时，不能假设最终正文自动返回给兄弟。Interrupted 不会生成 parent completion 邮件，`interrupt_agent` 的调用收据/UI activity 与完成正文是不同路径（测试 `multi_agent_v2_interrupted_turn_does_not_notify_parent`）。因此不能假定中断一个 child 必然唤醒另一个正在 wait 的 parent。该完成投递是 best effort，源码注释明确说明；Ki 继续保留已实现的持久 `CompletionIdentity` 与认领规则，不退化为 best effort，也不把已修复交付竞态再列为待修 bug。

典型流程：parent spawn → 做独立工作 → 需要结果时 wait → child terminal 邮件唤醒 wait → parent 的下一模型轮接收结果 → 若追加任务，followup 原 child。child 要请 parent 决策，使用 send_message → 自己 wait；不能 followup root，不能靠双方重复“收到/继续”等消息驱动循环。

**idle 的例外必须明确**：[session/handlers.rs](/data/hgy/codex/codex-rs/core/src/session/handlers.rs) 与 [tasks/mod.rs](/data/hgy/codex/codex-rs/core/src/tasks/mod.rs) 对 outstanding durable sleep 允许 queue-only 邮件启动恢复 turn。这是已登记等待的续接，不是普通 idle 邮件启动新任务。Ki 第一版先实现 active wait；durable sleep 不纳入首版范围。普通 idle parent 完成邮件持久保存并展示通知，不自动生成模型轮。

#### 3.1.5 Agent 生命周期、并发与失败清理

**agent 身份、active turn、驻留 runtime 是三件事。** 本地 v2 为此有两类容量管理：

- [execution.rs](/data/hgy/codex/codex-rs/core/src/agent/control/execution.rs) 限制正在执行的 v2 subagent turn，root 与 v1 不走这条限制。guard 挂在 active turn 上，释放时减 running count；已完成的 agent 身份不应永久占 running slot。spawn/trigger-task 的容量检查可能返回 `AgentLimitReached`；它不是无界排队服务。
- [residency.rs](/data/hgy/codex/codex-rs/core/src/agent/control/residency.rs) 管理已加载 child runtime 和 pending slot。容量满时尝试卸载已 Completed/Errored/Interrupted、没有 active turn、没有 pending mailbox 的 child；交互 gate 避免投递与卸载并发。没有可卸载对象时返回容量错误。
- 卸载保存可恢复历史/配置及环境选择，**不删除 logical task path**。消息发送可通过 [ensure_v2_agent_loaded](/data/hgy/codex/codex-rs/core/src/agent/control/spawn.rs) 按需恢复；`interrupt_agent` 则只检查已知身份，不为中断去加载。`list_agents` 仅列当前加载对象，所以不能用“列表缺失”判断“名称已经失效”。
- [interrupt.rs](/data/hgy/codex/codex-rs/core/src/agent/control/interrupt.rs) 中断一个 turn，不表示递归关闭整棵子树。v2 无公开 close 工具；root/session tree shutdown 属于 runtime 生命周期和 teardown，不等于 turn 自然完成或普通 interrupt。
- [spawn_guard.rs](/data/hgy/codex/codex-rs/core/src/agent/control/spawn_guard.rs) 的 `PendingSpawn` 在初始输入接受前保有清理责任；spawn 先等待父子 edge 写入任务/fork materialization，再交付初始任务，最后提交 reservation。edge 写入在无 graph store/ephemeral 时跳过，持久错误会记 warn，因此不是一个强事务保证。中途失败/取消需要关闭未成功启动的 child、处理正在写入的 edge、释放容量，防止 orphan 或“父子关系还没写完，关闭记录已先落盘”。

Ki 借鉴这些所有权与顺序，不复制 Codex 图存储、SQL、Rust Drop 或完整 runtime residency。第一版将 stable agent/session metadata 与 per-generation run 分开；执行许可采用 **原子 check-and-reserve**，不要照抄独立计数检查成为 Ki 的并发超卖窗口。内存卸载池是后续优化，不是完成核心协作能力的前提。

#### 3.1.6 “后台任务”与 subagent 的关系

Codex 中“后台”至少有三种含义；只有第一种属于这 6 个协作工具。

| 类别 | 执行内容与身份 | 与 subagent 的关系 | 等待/取消边界 |
| --- | --- | --- | --- |
| v2 child agent | 有独立 thread/session/history 的模型 loop；canonical task path + thread ID + turn ID | `spawn_agent` 本来就异步，不需要 `run_in_background` 参数。child 可以自己调用 shell 工具，也可在允许时 spawn 后代 | `wait_agent` 等 mailbox；`interrupt_agent` 中断当前 turn，身份保留 |
| 后台 terminal/process | 一个 OS/远端执行进程，有 stdout/stderr/stdin、exit code；模型工具将整数 process ID 称为 `session_id` | root 和 child 都可创建；它没有 agent mailbox、模型上下文、canonical task name。terminal `session_id` **不是** agent ThreadId/session ID | `exec_command` yield 后继续运行；`write_stdin` 读/写同一进程；关闭 terminal 独立于 interrupt agent |
| 内部 `SessionTask` / Tokio task | regular turn、compact、review、user shell 等内部异步任务 | [tasks/mod.rs](/data/hgy/codex/codex-rs/core/src/tasks/mod.rs) 的执行组织方式；不是每个 task 都是 subagent，也不是模型可操作的统一 TaskOutput 任务目录 | 由 Session active-turn/cancellation/lifecycle 管理；另有 clock sleep 等控制工具 |

本地 Codex 的 builtin 工具注册中没有 `TaskOutput` / `TaskStop` / `task_output` / `task_stop`。对应能力分别放在 **terminal 工具、agent 工具和 host 控制 API**，没有一个供模型使用的统一后台 task 工具族：

| 操作 | terminal/process | v2 subagent |
| --- | --- | --- |
| 创建并返回控制权 | interactive `exec_command`；仍运行则返回整数 session_id | `spawn_agent`；返回 canonical task_name |
| 继续观察/获取结果 | `write_stdin({session_id,chars:""})` 返回本次输出和运行/退出信息 | 完成正文通过 parent mailbox 交付；`wait_agent` 只等待活动，`list_agents` 查询状态 |
| 模型请求中断 | `write_stdin({session_id,chars:"\u0003"})` 表示 Ctrl-C；这是请求中断，不保证所有命令立即退出 | `interrupt_agent({target})` 中断当前 turn，保留 agent 身份 |
| host 列表/明确终止 | `CodexThread.list_background_terminals` / `terminate_background_terminal`；`Op::CleanBackgroundTerminals` 清理全部 | agent controller/session-tree teardown；与单 turn interrupt 分开 |

管理器是 **每个 Session 拥有一个 UnifiedExecProcessManager**，初始化在 [session/session.rs](/data/hgy/codex/codex-rs/core/src/session/session.rs)。其 ProcessStore 以整数 process ID 索引 ProcessEntry；entry 保存进程 handle、原始 call_id、命令/cwd、tty、环境与权限快照、last_used。进程 handle 管输出 buffer、exit state 和交互锁。一次模型工具调用 yield 只交回调用控制权，manager 继续持有进程；后续 write_stdin 按同一 ID 找到它。host 的 terminal 列表只列尚未退出进程，不是持久作业历史或崩溃后的恢复清单。

典型序列：exec 启动 → store 注册 → 有界等待 → 返回 live ID → watcher 持续收输出/记录退出 → write_stdin 读取并观察最终状态 → release；明确 terminate 或容量回收也可结束持有。模型侧 Ctrl-C 与 host 侧 terminate 不应合并成一个“必定杀进程成功”的收据。

后台 shell 源码入口：[exec_command.rs](/data/hgy/codex/codex-rs/core/src/tools/handlers/unified_exec/exec_command.rs)、[write_stdin.rs](/data/hgy/codex/codex-rs/core/src/tools/handlers/unified_exec/write_stdin.rs)、[unified_exec/mod.rs](/data/hgy/codex/codex-rs/core/src/unified_exec/mod.rs)、[process_manager.rs](/data/hgy/codex/codex-rs/core/src/unified_exec/process_manager.rs)、[async_watcher.rs](/data/hgy/codex/codex-rs/core/src/unified_exec/async_watcher.rs)。关键实现细节：

1. interactive exec 启动进程后先纳入 manager，再等待 yield budget；仍运行则返回输出片段和 process/session ID，已经结束则返回 exit code。`yield_time_ms` 是本次观察时长，**不是进程执行 timeout**；初次等待通常 clamp 至 250–30,000ms，Windows 有初始等待下限。
2. `write_stdin` 空 chars 为继续收集，非空为向 stdin 写入；空写最低等待 5s，最大等待受 background terminal 配置控制，默认 300s。普通非空写入要求 tty=true；tty=false 的进程 stdin 关闭，仅保留 Ctrl-C 特殊中断路径。它可以用于 PTY 交互，并不是发消息给 agent。同一个 terminal 的读写通过 interaction lock 串行，避免多个 waiter 争抢 drain buffer；不同 terminal 可以并行。
3. stdout/stderr 有持续 output watcher 和 exit watcher，发 `ExecCommandOutputDelta`/终态事件；保留输出有 1MiB 上限，单 delta 有 8192-byte 上限和 UTF-8 边界处理。结束状态可由事件观察，不需要模型反复询问进程“进度”。这些事件不是 v2 mailbox 邮件，`wait_agent` 不能直接等 shell exit。
4. manager 在初次 yield 前保有进程，源码明确为避免 **interrupt turn 导致最后一个 Arc 丢失而杀后台进程**。一般 interrupt 只调 `interrupt_task`；独立的 `CleanBackgroundTerminals`、`list_background_terminals`、`terminate_background_terminal` 负责 terminal 控制。因此 child 完成/interrupt 后，它启动的长命令仍可能继续，不能将两者的取消绑定假定为相同。
5. one-shot exec 与 interactive exec 不同：前者 timeout 后会终止，不能把全部 `exec_command` 都解释为永不终止的后台模式。后台进程也受 shutdown、明确清理、network denial 等生命周期管理，不保证跨进程重启恢复。
6. 本地 terminal manager 有软容量 64、最近 8 项保护、优先回收较旧已退出进程的策略，某些情况下会回收仍 live 的旧进程，交互锁占用时可临时超过软容量。Ki 不照搬这种自动杀进程的容量策略；容量不足必须给出明确可处理状态。

[clock.sleep](/data/hgy/codex/codex-rs/core/src/tools/handlers/sleep.rs) 也能订阅 input activity 提前返回，但它是时间等待控制，不是创建后台 job；durable sleep 的 idle 恢复分支另见 3.1.4。本方案不实现定时自动化或云端任务服务。

**对 Ki 的直接结论**：从 `internal/tools/jobs.go`（原实现，已移除） 重构 ShellProcessManager，从 [AgentStore](../../internal/agent/tasks.go) 与 server orchestration 整合 AgentController，移除 `internal/tools/task_tools.go`（原实现，已移除） 的模型工具耦合。typed identity 和取消路由分开；agent 等邮件、shell 等进程状态，可以共享 activity/revision/wait 基础设施。wait_agent 不等待 shell，terminal session_id 不填入 agent target。

Ki 已有 context-without-cancel、后台提升、进程树终止与输出存储，本轮不重复修复。需要补齐的是后台 child 默认异步，以及 turn interrupt / shell stop / subtree shutdown 三种操作的明确契约与测试。shell yield/stdin 完整实现已纳入 S0 核心切换；[工具后续优化](tools.md) 只保留其它独立 shell 事项。

#### 3.1.7 Ki 最终工具面与取舍

以下为拟实施接口，正式契约在实现时同步 docs/tools.md 与 owning doc.go。**直接采用六个 agent 协作工具名**，删除旧 Bash/PowerShell/Agent/SendMessage/TaskOutput/TaskStop；每个新工具接受 PascalCase 别名，只发布 snake_case schema，不增加组合 mode 工具。工具 namespace 是否采用 collaboration 由现有协议能力决定，同一 session 的工具集合不随 busy/idle/深度变化。

| Agent 协作工具 | 首版范围 |
| --- | --- |
| `spawn_agent` | stable task_name 与 fork_turns；默认异步，返回 canonical path 与有界 Ki identity。移除 parent Terminate 和 agent foreground promotion；不加模型覆盖、远端 backend 或强制 workspace fork |
| `send_message` | QueueOnly；普通 idle 不触发 run；Ki 可保留有界 summary/持久收据作为明确扩展 |
| `followup_task` | TriggerTurn；共享投递路径，拒绝 root；busy 接入与 idle generation 递增 |
| `wait_agent` | mailbox/steer 可唤醒；只返回唤醒元信息，正文走唯一通知路径 |
| `interrupt_agent` | 中断指定 child 当前 generation、保留身份；拒绝 root/self；与 host shell terminate/tree shutdown 分开 |
| `list_agents` | 有界树快照和 canonical path/status；明确 registered/running/resident 的含义，不把内存卸载等同身份消失 |
| `exec_command`（shell 独立工具） | cmd/workdir/shell/login/tty/yield/output budget；统一进程 manager，替换 Bash/PowerShell |
| `write_stdin`（shell 独立工具） | 空写收集、stdin/TTY、中断请求、exit code；不依赖 TaskOutput/TaskStop |

新模型工具里没有通用 Get/Wait/Stop task 族。shell 输出经 write_stdin，agent 结果经 mailbox；共享 revision/wait/事件底座即可，不共享混合结果认领和取消路由。不引入 resume_agent、close_agent 或 v1 兼容层。

可按顺序阅读 [handler tests](/data/hgy/codex/codex-rs/core/src/tools/handlers/multi_agents_tests.rs)、[execution tests](/data/hgy/codex/codex-rs/core/src/agent/control/execution_tests.rs)、[residency tests](/data/hgy/codex/codex-rs/core/src/agent/control/residency_tests.rs)、[input queue tests](/data/hgy/codex/codex-rs/core/src/session/input_queue.rs)。Ki 重建确定性 gate 测试；Codex 测试结果不代替 Ki 验收。

首版包含 shell 交互、执行容量、名称 reservation、spawn 清理、持久消息和基础恢复。驻留池、durable sleep、message board 是后续按需要实施的 Codex 特性；不是六个工具能正常工作的必要前提。M4 只负责显式资源协调和验证复用。

### 3.2 Claude Code：事件进度与控制消息

本地 `/data/hgy/claude-code-source-code`，基线 `19b097b6c9b5fdf7e34f7fdcb63c4351d6eb6003`。README 声明来自 npm `@anthropic-ai/claude-code` 2.1.88，且有 108 个缺失模块。它是提取/重建快照，不能当作完整官方仓库或最新发行版保证。

- [LocalAgentTask.tsx](/data/hgy/claude-code-source-code/src/tasks/LocalAgentTask/LocalAgentTask.tsx) 的 `ProgressTracker` 从 assistant/tool-use 更新计数，保留最多 5 项 recentActivities；`updateAgentProgress` 只更新 running task。Ki 可采用有界事件进度，直接复用工具名称/执行状态；不引入额外模型调用生成周期性进度摘要。
- [SendMessageTool.ts](/data/hgy/claude-code-source-code/src/tools/SendMessageTool/SendMessageTool.ts) 支持普通字符串与 shutdown/approval 等结构化控制消息，summary 用于 UI；[teammateMailbox.ts](/data/hgy/claude-code-source-code/src/utils/teammateMailbox.ts) 有未读消息和加锁写入。借鉴控制和正文分离；Ki 无需复制 team mailbox 文件和跨进程文件锁体系。
- [TaskOutputTool.tsx](/data/hgy/claude-code-source-code/src/tools/TaskOutputTool/TaskOutputTool.tsx) 的等待每 100ms 轮询，描述标记 deprecated、推荐 Read 输出文件。Ki 采用 write_stdin 的 shell 输出观察和 wait_agent 的 Go channel 协作等待，不照搬轮询或让主 agent 解析整个 child JSONL。

### 3.3 Pi：小核心、明确输入时机、有界并行

公开源码访问于 2026-10-01，仓库由 `badlogic/pi-mono` 跳转到 `earendil-works/pi`，固定快照 `8ce69e9d2b171d173fe4b6b2b6256f1f4411e69d`。

[agent-loop.ts](https://github.com/earendil-works/pi/blob/8ce69e9d2b171d173fe4b6b2b6256f1f4411e69d/packages/agent/src/agent-loop.ts) 区分工具轮间 steering 和即将停止时的 follow-up；这支持 Ki 在现有 busy delivery 上补充 agent 消息语义。[subagent 扩展示例](https://github.com/earendil-works/pi/blob/8ce69e9d2b171d173fe4b6b2b6256f1f4411e69d/packages/coding-agent/examples/extensions/subagent/index.ts) 有 single/parallel/chain、增量进度和并发上限；它是示例扩展，不是 Pi 核心内建调度器。Ki 借鉴有界并行与低成本进度，不复制固定 4 并发或新工作流 DSL。

### 3.4 OpenCode：显式续跑和后台生命周期

公开 [task.ts](https://github.com/anomalyco/opencode/blob/0112a92c416f5ad833d96e7a8308441f0a875d94/packages/opencode/src/tool/task.ts)，访问于 2026-10-01，固定快照 `0112a92c416f5ad833d96e7a8308441f0a875d94`。

该实现用显式 task_id 复用 child session，向工具 metadata 发布 child session 标识，且独立处理后台执行/注入结果。借鉴可查询的稳定身份与显式追加工作；不引入 provider/model 覆盖、权限体系或另一套后台结果注入机制。它同样会把后台完成结果送回 parent，因此不是“杜绝所有自动唤醒”的现成答案。

## 4. M0：代次统计与协作诊断（已实现）

runEmitter 为 child 绑定 taskId/generation/runId，AgentController 按 RequestHeader、MessageEnd、工具 start/end 和 AgentEnd 归约。结果只取本代次 assistant 正文；空结果不借用旧答复。工具 attempt 按 callId，usage 的 input/output/cache-read/cache-write/total 与最近 contextTokens 分列；runStats 与 lifetimeStats 分开。恢复旧 metadata 标记累计不完整。

session inspect/trace 增加 sideband runtime 与时间诊断，工具和 wait_agent 采用区间并集，未归因区间保留 unknown；没有将不同 agent 的 duration 相加当 root elapsed。telemetry 的工具记录增加 generation、requestedName、mode receipt 和 wakeReason；queued follow-up 的 acceptedAt 与 queueWaitMs 保留排队时间。

## 5. S0：exec_command/write_stdin（已实现）

ShellProcessManager 独立保存 numeric handle、owner Ki session、run/tool-call/agent/generation、command/cwd/tty/status、revision 与完整输出位置。exec/write_stdin 使用 Start/Interact，观察预算和停止独立，取消观察不杀进程。Unix PTY、Windows ConPTY 真实交互；pipe 普通输入明确拒绝。每 session live cap 64，完整输出 spool、1MiB 增量 buffer、UTF-8 chunk carry、ANSI 清理与输出预算均有测试。session/tree shutdown 明确终止进程树，handle 不跨 server 重启恢复。

参数与生命周期的正式表见 [工具契约](../tools.md)。shell 参考本地 Codex shell_spec.rs/unified_exec.rs/shell.rs，省略 shell 时 Windows 优先 PowerShell→Git Bash，Unix 使用发现的 Bash；login 分平台映射，保留 native exit code、代理环境和 extension PATH。

## 6. M1：工具面、稳定 agent 与显式任务投递（已实现）

spawn_agent/send_message/followup_task/wait_agent/interrupt_agent/list_agents 共享单一 AgentRuntime 与 AgentController。具名路径按 root 校验与保留；fork_turns=all/none/N 复制完整已完成 turn。root active child 默认 4，不设固定 depth cap，完成身份持续可寻址。

send_message QueueOnly：先持久化 context queue 再唤醒 Inbox，不启动 idle run。followup_task TriggerTurn：稳定 clientRequestId 的 pending，在 busy/容量满时排队；pending→active generation 在同一次 metadata 更新交接。interrupt 清除当时 pending 并保留身份，之后接受的显式 follow-up 可开启新代次。schema/前缀不随 busy/idle/depth 改变。

## 7. M2：mailbox 等待与后台边界（已实现）

wait_agent 只观察调用方当前 Inbox，订阅和 pending 检查共用锁；用户 steer、普通消息与完成通知唤醒，timeout/cancel 不停止其它 agent，也不认领完成结果。wait 返回 wake_reason；loop 仍配对完成整个工具批次后再 drain。

child turn 独立于 parent；中断 child 不杀 owned shell。进程进度不会自动触发模型/mailbox轮次。完成通知只发结构 parent，按 task/generation 在 transcript persistence 做 ledger 去重；idle parent 留下上下文，无自动工作轮次。队列/transcript/metadata 没有跨文件 crash transaction。

## 8. M3：进度与恢复（已实现）

AgentProgress 含 revision、phase、lastActivityAt、最多 8 个 currentTools、waitingFor、runStats、lifetimeStats 与 pendingTasks。阶段为 starting/executing/waiting_message/waiting_resource/settled/interrupted；只有执行/输入事件更新活动时间，无 heartbeat 判死策略。旧 generation callback 被拒绝，不按 token 写 metadata。

进程与 agent 的更新是 sideband loop.Event/JSONL，经当前 run 和 global SSE 发布；session GET 与 list_agents 返回 bounded snapshots。WebUI 在 composer 上方显示进程/agent阶段、统计和停止操作，移动按钮 44px；重新打开/断线恢复使用同一 GET 投影，旧 revision 不回滚新状态。runtime 不改变 own-run busy 语义或 activeDescendantCount。

server 恢复先载入全部身份，再解析稳定路径并恢复 pending。原 executing/waiting current turn 改为 interrupted，当前 OS handle 不恢复。shutdown 等待 runner 的完成回调之后再关闭 process managers，避免晚到进度重新创建目录/manager。

## 9. M4：按测量实施的资源协调

### 9.1 先记录，再决定租约

现有 [e2e-parallel.ts](../../web/scripts/e2e-parallel.ts) 已隔离 runDir/报告并校验执行覆盖；这些成果保留。人工“槽位”消息只能证明协调成本，不能证明需要串行化所有 browser/go test。

- [ ] 给验证记录保存 command、cwd、环境摘要、source/build fingerprint、完整结果和输出引用。fingerprint 覆盖实际工作树输入和构建产物，不只看 Git HEAD，未提交修改同样会失效。重复任务发现等价已完成记录时明确返回已验证结果；source/env/browser/project/fixture 任一 relevant 输入变化则失效。
- [ ] 先做显式结果复用，不自动阻止命令，不按 command 文本相同就复用。未结束任务的附着只共享观测，不让第二个调用方自动获得停止原任务的所有权。
- [ ] immutable binary/dist 与唯一报告目录由测试脚本继续负责；harness 不自动解析任意 shell 命令识别读写集。

### 9.2 可选资源租约

若测量证实仍有冲突，可给 shell 执行增加显式 `resources` 声明，支持 workspace 级和 host 级 scope、容量和独占。例如共享 build 输出按 canonical workspace 独占、browser 容量可配、perf 测量在 host 级独占竞争性重型检查。它是协作调度，不是安全隔离；未声明 exec_command 写入仍不受保证。

MVP 只为一次 shell job 获取全部资源并在启动失败/完成/明确 process terminate/Shutdown 时释放；采用稳定排序和全量获取，避免持有一半资源再等待另一半。等待阶段必须可取消、可被新输入观察。禁止 parent 持有资源后等待需同资源的 child。别先增加能跨多轮任意持锁的模型工具。

active-child 执行上限在 M1 核心实施；M4 只协调 shell 的竞争资源。深度限制不能替代宽度限制；不得让占有 execution slot 的 parent/child 无限排队等待同类 slot。记录 root 范围峰值与容量错误再调默认值；资源不足不照搬 Codex terminal LRU 自动杀 live 进程。

租约为 process-owned；重启先把旧任务标 interrupted，不能把磁盘上的旧“持有”状态重新当成有效许可。Windows/macOS/Linux 路径和取消行为均纳入检查。

## 10. 验收与实施检查

### 10.1 完整工具面切换

- [x] 同一 runtime 的 shell/agent 执行域只注册 exec_command、write_stdin 与六个 agent 协作工具；删除旧 shell/task/agent 工具实现和 composite TaskStore，不以 wrapper 继续调用旧生命周期。Catalog、执行 registry、extension builtin allowlist/执行桥与 tool toggle 的可见名称完全一致。
- [x] 更新 prompt、async note、CLI/WebUI 展示和测试 fixture；历史 transcript 中六个旧名称仍可显示，但重放/新模型调用不能借历史记录执行旧入口。参数不合法/旧名称直接返回明确错误。
- [x] 对工具开关与 agent pending 的语义变化采用 internal/state 迁移，保留原禁用/已接受工作意图。覆盖较新版本文件拒绝/不覆盖、迁移后重启和显式关闭工具的路径；不靠保留旧工具 alias 完成迁移。
- [x] 验证 exec_command 与 write_stdin 都受同一 session owner、资源环境和工具开关约束；终端 session_id 与 canonical agent target 混用应在 mutate 前拒绝。更换解释器不能跳过 shell 能力的禁用策略。
- [x] S0 与 M1/M2 内部可以分 commit 开发，完整工具面通过集成验收再切换默认注册；不交付“删 TaskOutput 但仍返回旧 task_id”或“删 TaskStop 但 manager 不负责关闭”的中间状态。

### 10.2 生命周期与协作回放

所有核心行为用 gate/channel 驱动的 scripted runtime 检验，禁止依赖真实模型或固定 sleep 来制造竞态。

| 场景 | 必须满足 |
| --- | --- |
| completed child 收到 send_message，随后重启 | 不自动续跑；消息可恢复，下一次 followup_task 只消费一次 |
| 同时 send_message/followup_task、active→idle 边界 | 来源、ID、顺序和 context boundary 一致；不丢指令、不多跑一轮 |
| parent wait_agent，任一 child 完成或用户 steer | 及时唤醒；其它 child 继续；结果只沿唯一通知路径交付 |
| 通知到达在 subscribe 前/后或 snapshot 与 select 之间 | 无丢唤醒，无忙等；在同一 critical section 的次序有明确测试 |
| child 提问并等待 parent，parent 等 child | parent 取得问题后可以决策；不等满 10 分钟才响应 |
| mailbox wait 收到多个更新/既有 pending/仅 progress | 多邮件不丢顺序；既有 pending 立即返回；progress 不触发模型轮 |
| caller wait 取消并续跑，child 同时 follow-up/interrupt | caller 旧 waiter 释放；child 旧进度/结果不覆盖新 run；中断与正文通知区分 |
| 续跑前后发生 compact、继承历史或空正文 | 本次统计不累加旧历史，本次结果不回退旧正文 |
| WebUI 断线重连、compact keep=0、多 child 并行 | 进度恢复一致，不生成新人工 turn，不展开正文或下载整棵树 |
| 同名并发 spawn、容量满、启动失败/中途取消 | 名称和许可不超卖；失败释放一次；无 orphan，root 可继续协调 |
| child 向 root follow-up、self/root interrupt、跨 tree target | mutate 前明确拒绝；send_message 给 parent 的决策路径可用 |
| idle parent 收到完成；parent 自然结束/child interrupt 时 shell 仍运行 | completion 持久但不启动模型；child/shell 身份与取消分离；Shutdown 收敛 |
| all/none/N fork 与重复 follow-up | 上下文/配置继承区分；tool 配对完整；stable 名称不变，generation 正确递增 |
| queue 满、持久写失败、取消/删除/Shutdown | 收据明确失败；无 lease/subscription/goroutine 泄漏 |
| 不同 workspace 独立任务、同资源等待 | 独立工作可并行；取消排队任务不占许可；覆盖校验和测试预算不放宽 |

M0 定义基线统计后，给 M1–M3 做确定性协作回放：普通 idle QueueOnly（包括 completion）导致新 run 的次数必须为 0；进度更新导致模型轮次必须为 0；gate 已释放且无其它执行工具时，wait 能在测试截止前结束，不依赖原 timeout；旧交付竞态的行为覆盖在新通知路径继续通过。

效率评价分别统计 followup_task 启动/排队、QueueOnly delivery、模型轮数、等待唤醒次数、交付延迟和输出字节。不得用“消息数越少越好”淘汰有效问题，不对昨天真实模型耗时承诺百分比改善。新的消息/进度路径必须保持统一输出上限；必要时增加控制面事件预算，按测量定值，不复用 provider性能阈值。

每阶段开发先跑 affected Go/Bun checks；S0/M1/M2 早期跑 scripted CLI/server 集成，M3 增加 responsive/重连相关 Playwright。交付时构建新 web/dist 后执行完整 embedded regression，避免再单独重复已经被 Go suite 包含的浏览器测试；perf 只在相关变化后无竞争运行。维护具体命令、结果和基线，不把验证计划标成通过。

## 11. 代码边界、文档与待办归属

| 所有者 | 实施职责 | 必须更新的契约 |
| --- | --- | --- |
| `internal/tools` | 六个 agent 协作工具与 exec_command/write_stdin、ShellProcessManager、AgentController 接口、typed 结果与 generation | `internal/tools/doc.go`、[tools.md](../tools.md) |
| `internal/server` | canonical agent registry、投递/input gate、spawn cleanup、执行容量、恢复投影 | `internal/server/doc.go`、[architecture.md](../architecture.md) |
| `internal/loop` | Inbox activity subscription、wait scope、事件定义；仍只 emit | `internal/loop/doc.go`、[events.md](../events.md) |
| `internal/session` / `internal/types` | context sequence、metadata 和稳定身份、按 run 分析 | 两个 owning `doc.go`、[session.md](../session.md)、[state.md](../state.md) |
| `internal/telemetry` / `internal/cli` | 协作时间与查询诊断 | owning `doc.go`、[session.md](../session.md) |
| `web/src/api` / `lib` / chat / sessions | typed snapshot、进度投影、中英文和移动 UI | [webui.md](../webui.md)；`unit` 与相关 `e2e` |
| `web/scripts` / `e2e` | 可选验证记录与资源声明、覆盖和隔离 | [webui.md](../webui.md) 与 owning package `doc.go` |

本文件拥有 shell/agent 生命周期切换、协作/等待/进度/统计的实施清单；[工具后续优化](tools.md) 保留环境快照/额外解释器等独立事项并链接这里。实现后迁移正式契约、勾选/删除相应待办；实际重现并修复的陷阱在 fix site 写 English why-comment，重复发生的问题再补 `docs/postmortem/`，不把方案当成已完成复盘。


## 12. 实施与验收记录（2026-10-01）

本轮完成 M0、S0、M1、M2、M3。旧共享 task 生命周期和 foreground promotion 已移除，六个 agent 协作工具与 exec_command/write_stdin 成为默认注册。所有 builtin 和 extension 的模型名称规范为 snake_case，只保留对应 PascalCase/native spelling 的执行别名；protocol call/result 保留实际调用名称，hook/telemetry/控制面使用 canonical 名称。

补充的生命周期边界：tree abort 在遍历前按 root/path 临时阻止 spawn 和 follow-up，先停止 runner 再停止 owned processes；不会波及 sibling 或其它 root，之后仍可使用原身份继续任务。Unix PTY 使用 pollable descriptor，Windows ConPTY 关闭以 once 保护；堵塞的 stdin 写入不阻塞 observer cancellation，终端关闭释放输入锁。输出 spool 失败即使进程 exit=0 也归类为 harness I/O 错误。显式 shell 名称复用 discovery 找到的解释器，支持不在 PATH 的 Git Bash/PowerShell。

| 验证 | 最终结果 | 范围 |
| --- | --- | --- |
| `cd web && bun run typecheck` | 通过 | WebUI 类型 |
| `cd web && bun run build` | 通过 | 构建新 web/dist，供 embed 使用 |
| `go test -tags embed -count=1 ./...` | 通过 | 完整 Go、本地 CLI/server、Bun unit 与 Playwright regression，含 responsive matrix |
| `go test -race ./internal/tools ./internal/server` | 通过 | 最终 process/agent ownership、admission、shutdown 与队列路径 |
| `go test -race ./internal/loop ./internal/session ./internal/extension` | 通过 | registry/Inbox、jsonl/sideband 与 extension alias 路径；后续修改未触及这些实现 |
| `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/ki-tools-windows-amd64.test.exe ./internal/tools`；同参数编译 `./internal/server` | 通过 | Windows 编译与测试编译 |
| `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c -o /tmp/ki-tools-darwin-arm64.test ./internal/tools`；同参数编译 `./internal/server` | 通过 | macOS 编译与测试编译 |
| `git diff --check`、改动文档的本地链接检查 | 通过 | 格式与已移除旧源码链接 |

确定性覆盖包含：完成/idle context 不启动新 turn、mailbox 超时/取消不停止 child、容量按 root 分离与释放后调度 pending、busy follow-up 的 generation 交接、重复完成通知去重、完整 turn fork、当前/累计统计与旧代次拒绝、PTY 输入与增量 UTF-8/full raw spool、观察取消后的显式 process/tree stop、清理屏障及稳定身份复用。WebUI 额外覆盖 390px/1280px 下的进度恢复、44px 控制按钮、准确的停止对象及旧 revision/generation 回放。

Windows/macOS 本轮只有交叉编译，未执行原生运行时测试。M4 的资源租约、验证复用及其它可选 Codex 增强保持待实施；本轮没有模型/provider 性能对比，也不据 scripted regression 承诺真实任务耗时改善比例。
