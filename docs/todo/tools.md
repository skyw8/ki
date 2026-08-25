# 工具后续优化

本文按参考实现分别记录尚未完成或只完成一部分的工具优化。已落地的行为统一记录在 `docs/tools.md`，不在这里重复维护完成清单。

范围只包括执行、输出、状态、格式、并发和交互；暂不包括安全、权限和 sandbox。

## Pi

参考：`/data/hgy/pi`。

### 可选工具

- [ ] `Ls`：Bash 已可替代，只有需要固定格式和更小上下文时再引入；优先级低。
- [ ] `Find`：现有 Glob 已覆盖主要需求，除非需要兼容 pi 命名；优先级低。

## Codex

参考：`/data/hgy/codex`。

### Unified exec

- [ ] 将“等待多久返回”和“命令最多运行多久”拆开：等待到期返回 live session，运行上限才终止进程。
- [ ] 提供与 `write_stdin` 等价的继续接口，支持轮询、写 stdin 和获取最终退出状态。
- [ ] 支持 TTY、显式 shell/login、环境变量和输出预算等执行参数。
- [ ] 为同一 session 保存必要的 shell environment snapshot，而不是每次仅重新执行 login shell。
- [ ] 扩展 `shellSpec` 的解释器类型和参数构造，覆盖 Unix sh/zsh、Windows CMD 和显式 shell 路径。该项只在出现对应使用需求时实施，不要求增加多个模型可见工具。

### 文件变更与并发

- [ ] 文件工具结果增加机器可读状态，同时保留简短的模型可读摘要。
- [ ] 将只读工具和文件变更工具显式分类：只读操作可并行，同一路径的变更必须串行。
- [ ] 为取消、timeout、后台完成和长任务建立跨工具统一的结构化事件。

### 可选工具

- [ ] `Plan`：为长任务和 WebUI 提供结构化步骤及状态；优先级中。
- [ ] `RequestUserInput`：WebUI 需要结构化选项交互时引入；优先级中。
- [ ] `ViewImage`：现有 Read 已能读图，先完成缩放；优先级低。
- [ ] `ToolSearch`：工具数量显著增长后再按需加载；优先级低。
- [x] `Agent` / 多代理：已接入 `forkMode=tree` child session、前台/后台 task、
  `TaskOutput`/`TaskStop` 统一生命周期；未完成的 agent definitions、worktree/team
  能力见 [multi-agent.md](multi-agent.md)。
- [ ] `Sleep` / `CurrentTime`：只有需要无副作用等待或取时才引入；优先级低。
