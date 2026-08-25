# Multi-agent

参考 Claude Code 的 `AgentTool`、`TaskOutputTool`、`TaskStopTool`、
`SendMessageTool` 和 `resumeAgent` 实现。当前 ki 已完成普通 Agent subagent 的
delegation、live steer、同 child 续跑和重启恢复：

- `Agent` 使用 Claude Code 形状的 `description`、`prompt`、`subagent_type`、`model`、
  `run_in_background` schema。主会话为 Agent 深度 0，最多允许 3 层 child；深度 3
  的 child 不再暴露 `Agent` 工具，直接 runtime 调用也会被拒绝。
- child 从 parent 当前 leaf 使用 `session.ForkAt(..., forkMode=tree)` 创建，复制可见历史
  和附件，随后运行自己的 loop、工具集、配置和 `events.jsonl`。
- 前台 child 返回 `completed` 结果；后台 child 返回 `async_launched`，并在完成后向
  parent 的 durable queue 注入 task notification。
- `TaskOutput` 和 `TaskStop` 通过统一 task store 支持 agent task；`model` 只修改 child
  的 session config。

明确不实现：`cwd` override、worktree isolation、远程 agent 和 Agent Teams 的 roster。
其中 `cwd`/`isolation` 不放入模型可见 schema，避免 provider 自动生成未支持参数导致
child 在 fork 前失败。
child 始终继承 parent cwd，隔离依靠 session tree。

## Background agent 续跑

### Claude Code 行为

Claude Code 的后台 Agent 返回稳定的 `agentId`。master 使用 `SendMessage` 继续同一个
agent，而不是重新调用 `Agent`：

```json
{
  "to": "agentId",
  "summary": "补充测试修复",
  "message": "请继续处理失败测试并汇报结果"
}
```

消息路由分两种情况：

- child 正在运行：消息进入 pending message queue，在当前 tool round 结束后的下一个
  model round 注入；不打断正在进行的 provider 请求。
- child 已完成、暂停或被停止：从 child 的持久化 transcript 恢复 session，在同一
  `agentId` 下启动新的后台 run。新的 prompt 会追加到原 child transcript，而不是创建
  新的 sibling。

这对应源码中的 `SendMessageTool`、`queuePendingMessage` 和 `resumeAgentBackground`：

- `/data/hgy/claude-code-source-code/src/tools/SendMessageTool/SendMessageTool.ts`
- `/data/hgy/claude-code-source-code/src/tasks/LocalAgentTask/LocalAgentTask.tsx`
- `/data/hgy/claude-code-source-code/src/tools/AgentTool/resumeAgent.ts`

### ki 实现方案

- [x] 增加内置 `SendMessage` 工具，第一阶段只支持 `to=agentId`；不依赖 team roster。
  文本消息沿用 Claude Code 的 `to`、`summary`、`message` schema。
- [x] 将 `AgentStore` 的一次性 task 记录拆成稳定 agent 记录和每次运行的 run 状态：
  稳定记录保存 `agentId`、child session ID、parent session ID、description、model、
  outputFile 和最近一次状态；run 状态保存当前 cancel、done、started/finished 时间。
- [x] 为 running child 保存可寻址的 `loop.Inbox`。`SendMessage` 找到 live child 后把
  消息 push 到 Inbox；现有 loop 已在当前 stream/tool round 后 drain Inbox，可复用这条
  steer 语义。
- [x] 为 idle/completed/stopped child 增加 resume 路径：复用原 child session ID，打开
  原 transcript，在原 leaf 追加新的 user prompt，再调用现有 `runPrompt`；不得再次
  `ForkAt`，也不得生成新的 agent ID。
- [x] 明确消息竞态：消息到达完成边界时，优先落到持久化 queue；只有确认 live run 仍由
  同一个 runState 持有时才直接 push Inbox。这样不会出现消息已返回成功但 child 已
  退出、消息丢失的问题。
- [x] `TaskStop` 只停止当前 run，不删除稳定 agent 记录；后续 `SendMessage` 可以从
  原 child session resume。删除 session 时才清理 agent 记录和未消费消息。
- [x] child 每次续跑完成后继续向 parent durable queue 发送 completion notification，
  并让 `TaskOutput` 返回同一个 agent/task ID 的最新状态和结果。

## task/session 重启恢复

### 实现前的问题（已解决）

child 的 `events.jsonl` 是持久化的，但旧版 `AgentStore` 只有进程内 map。server 重启
后，task ID、child session ID、parent 关系和运行状态的索引都会丢失，因此原 task ID
不能继续用于 `TaskOutput`、`TaskStop` 或续跑。

### 实现方案

- [x] 在 child session 旁增加 agent metadata（建议 `agent.json`），持久化稳定
  `agentId`、task ID、parent/child session ID、description、model、outputFile、状态和
  最近一次 run 信息；cancel function、goroutine、provider 连接等进程对象不持久化。
- [x] server 启动时扫描 session index 和 agent metadata，重建 `AgentStore` 的稳定
  agent 索引。已完成 child 从 metadata 恢复最近结果，续跑时以 transcript 作为上下文；
  server 崩溃时处于 running 的 child 标记为 `interrupted`，不能伪装成仍在运行。
- [x] `TaskOutput` 能查询 `completed`、`stopped`、`interrupted` 等恢复状态；
  `TaskStop` 对不存在的 live run 返回稳定状态；`SendMessage` 对 interrupted/stopped
  agent 走同一 resume 路径。
- [x] 续跑消息先写入 child agent metadata 的 durable pending queue，再启动 run；server
  在恢复扫描后重新 dispatch 未消费消息。parent completion notification 复用现有
  `session.Enqueue` / queue locking，不另造 REST 数据路由。
- [x] parent 的 completion notification 继续写入 parent session queue；恢复后不能
  重复发送同一个 run 的通知，需要为 run 保存幂等 ID。
- [x] session 删除、server 正常关闭和 agent metadata 清理保持一致：删除前移除
  registry 记录并取消 runner，metadata 随 child session 一并删除；启动扫描只信任
  `session.List` 返回的 child 目录，不会把目录外的孤儿 metadata 加入 registry。

## 并发、递归 fork、取消和生命周期测试

这些能力的单元、server 和 e2e 边界覆盖已补齐：

- [x] 并发启动多个 background child：每个 child 有独立 tree session、task/agent ID、
  output 和 parent notification，通知不能串写或丢失。
- [x] running steer：provider 在 tool round 中阻塞时发送 `SendMessage`，确认消息在
  当前 round 后按 FIFO 注入，并且不会注入到下一次错误的 child run。
- [x] completed/stopped resume：续跑保持同一个 agent ID 和 child session，transcript
  追加新 prompt，结果和通知只对应当前 run。
- [x] 消息与完成/停止并发竞态：覆盖 send-vs-complete、send-vs-stop、resume-vs-delete，
  验证消息不会丢失、重复执行或发送给已替换的 runState。
- [x] server restart recovery：重启后能重建 agent/task 索引，查询原 task ID，并通过
  `SendMessage` 恢复 interrupted child。
- [x] 递归 fork：parent → child → grandchild 使用 `forkMode=tree`，停止或删除父节点
  时验证子树的任务、进程和 session 清理顺序。
- [x] 递归深度保护：主会话深度 0，允许 child 深度 1–3；深度 3 不再暴露 `Agent`，
  `SpawnAgent` 入口同时拒绝超限调用。
- [x] 同一 workspace 下并发 child session 写入、多个 child 同时结束、重复 `TaskStop`
  和重复 completion notification 的边界行为。

## Agent Teams（后续范围）

Agent Teams 不属于普通 Agent subagent 的续跑方案。若以后实现，再增加：

- `TeamCreate` / `TeamDelete` 团队生命周期。
- `Agent(name, team_name, mode)` 命名 teammate 和扁平 roster。
- `SendMessage` 的 teammate name、broadcast、shutdown/plan response 路由。
- `TaskCreate` / `TaskGet` / `TaskList` / `TaskUpdate` 共享 task list。
- team mailbox、owner 通知、idle/wakeup、团队 UI 和 shutdown 协议。

roster 不是单独的 `TeamRoster` 工具，而是团队状态中的 `members` 持久化数据。普通
Agent 的第一阶段 `SendMessage` 只按 agent ID 路由，不引入这些团队概念。
