# 精简 `/goal` 扩展

项目级 sidecar：`.ki/extensions/goal/`（TypeScript / Node）。不 curl `/v1`，不贡献前端。契约见 `docs/extension.md`。

## 做什么

会话内一个目标，idle 边界自动续跑，直到完成、阻塞、暂停或达到续跑上限。

| 入口 | 行为 |
|---|---|
| `/goal <objective>` | 启动；已有未完成目标则拒绝（`/goal clear` 或 `/goal edit`） |
| `/goal` / `status` | notice + 刷新 chip/panel |
| `/goal pause` `resume` `clear` `edit <text>` | 暂停 / 续跑 / 清除 / 改目标 |
| 面板按钮 | 同上（`clear` 走 `ui.confirm`） |
| `goal_complete` / `goal_blocked` / `goal_wait` | 模型收口；`goal_id` 防过期调用 |

续跑：`agent_settled` 后 `session.enqueue`（`when=settled`，`deliverAs=queue`）。每个 occupy 的 `before_agent_start` 把目标块 **追加进 system 全文**（steer 不重跑该事件，也不用 `context` 冒充每 turn）。Kickoff / resume / edit 用 `command.invoke` 的 `prompt` 走用户 occupy，避免在 15s 命令超时里再开一轮。

上限：自动续跑 25 次后暂停。发给模型的提示词照搬 pi-goal（`buildGoalPrompt` / system / continue / edit / resume / wait 规则）。无 token 预算、无 mutex / bus、无设置页。 `goal_wait` 只做「停续跑 + 可选 `resume_after_ms` 唤醒」，不是完整 pi-goal wait 协议。

## 包

```
.ki/extensions/goal/
  extension.json    command=node --import tsx src/index.ts；install=npm install
  src/              源码直跑，不编成单文件
```

订阅：`before_agent_start` sync，`agent_settled` async。工具在 `initialize` 申报，始终可见。

## 状态

Host 无 custom-entry 读回 RPC。sidecar 写 `{KI_HOME}/goal/{sessionId}.json` 做 reload 恢复；同时 `session.appendEntry`（`goal-state`）进 jsonl（轨迹用，sidecar 不读）。fork 新 session 不继承。initialize 之后若仍 active 且 idle，补一次续跑。

chip / panel 不持久化；绑定后按文件重放 `ui.setStatus` / `ui.setPanel`。

## 不做

pi-goal 的预算 / 无进展启发式 / workflow mutex / managed-run RPC / 工具显隐策略 / 设置 UI / 压缩恢复。

## 验证

`cd .ki/extensions/goal && npm test`。Host 会在拉起 sidecar 前 `npm install`。手工：`ki serve` 后 `/goal …`。
