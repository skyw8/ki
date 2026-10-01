# 工具后续优化

本文按参考实现分别记录尚未完成或只完成一部分的工具优化。已落地的行为统一记录在 `docs/tools.md`，不在这里重复维护完成清单。

范围只包括执行、输出、状态、格式、并发和交互；暂不包括安全、权限和 sandbox。

exec_command/write_stdin 替换 Bash/PowerShell、六个 agent 协作工具替换旧 Agent/SendMessage/TaskOutput/TaskStop，以及 mailbox 等待、后台执行、进度和 generation 统计的调研、实施顺序及验收见 [Harness 协作与长任务调度改进方案](harness-agent-coordination.md)。此处不重复维护该清单。

## Pi

参考：`/data/hgy/pi`。

### 可选工具

- [ ] `Ls`：shell 执行已可替代，只有需要固定格式和更小上下文时再引入；优先级低。
- [ ] `Find`：现有 Glob 已覆盖主要需求，除非需要兼容 pi 命名；优先级低。

## Codex

参考：`/data/hgy/codex`。

### Unified exec

exec_command/write_stdin 的工具替换、PTY/stdin、shell/login/workdir、yield 与执行期限分离、输出预算及后台生命周期由 [协作方案 S0](harness-agent-coordination.md) 统一实施。下面只保留不阻挡此次切换的独立扩展；不维护第二份实施清单。

- [ ] 为同一 session 保存必要的 shell environment snapshot，而不是每次仅重新执行 login shell。
- [ ] 扩展 `shellSpec` 的解释器类型和参数构造，增加基础解释器之外的类型/方言支持，例如 Windows CMD；S0 已要求的默认解释器和显式 shell 路径解析不再列为可选。扩展只在出现使用需求时实施，继续复用 exec_command。

### 文件变更与并发

取消、timeout、后台完成和长任务的结构化事件统一纳入 [协作方案 S0/M3](harness-agent-coordination.md)。已实现的文件结果与 mutation queue 契约见 [tools.md](../tools.md)。

### 可选工具

- [ ] `Plan`：为长任务和 WebUI 提供结构化步骤及状态；优先级中。
- [ ] `RequestUserInput`：WebUI 需要结构化选项交互时引入；优先级中。
- [ ] `ViewImage`：现有 Read 已能读图，先完成缩放；优先级低。
- [ ] `ToolSearch`：工具数量显著增长后再按需加载；优先级低。
- [ ] `Sleep` / `CurrentTime`：只有需要无副作用等待或取时才引入；优先级低。
