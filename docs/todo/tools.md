# 工具后续优化

本文按参考实现分别记录尚未完成或只完成一部分的工具优化。已落地的行为统一记录在 `docs/tools.md`，不在这里重复维护完成清单。

范围只包括执行、输出、状态、格式、并发和交互；暂不包括安全、权限和 sandbox。

## Claude Code

参考：`/data/hgy/claude-code-source-code`。

Claude Code 的 Bash、PowerShell、后台任务、`TaskOutput`、`TaskStop` 和 `Monitor` 主体生命周期已经引入。剩余工作是约束这些能力产生的模型上下文。

### 后台任务输出

- [ ] 限制 `TaskOutput` 返回的完整输出大小；超限时只返回尾部、截断统计和现有 `output_file`，避免把整份日志重新放入 context。
- [ ] 将任务 timeout、取消、退出码和截断状态统一为结构化结果，减少 Bash、PowerShell、TaskOutput 和 Monitor 各自拼接状态文本。

## Pi

参考：`/data/hgy/pi`。

### Read

- [ ] 在每个异步文件操作前后检查取消信号，避免取消后继续读文件或生成结果。
- [ ] 返回结构化截断信息：`truncated`、总字节数、总行数和下一次读取位置。
- [ ] 为超过 50KB 的单行提供可执行的分段读取方式；当前 `offset` 只能按行继续。
- [ ] 图片进入模型前按模型支持尺寸缩放，避免直接发送过大的 base64。
- [ ] 将文件读取抽象为可替换 operations，支持远程或虚拟文件系统；优先级较低。

### Write / Edit

- [ ] 对同一路径的 Write、Edit 和 apply_patch 使用共享 mutation queue；不同文件仍可并行。
- [ ] 文件操作在目录创建、读取、写入及异步等待前后检查取消；取消后等当前原子步骤结束再释放路径锁。
- [ ] Edit 支持一次提交多个 `{oldText,newText}`；所有替换基于同一份原文校验唯一性和重叠，再一次写入。
- [ ] Edit 的模型可见 `content` 只返回替换数和路径；统一 diff、patch 和首个变更行放入结构化 `details` 供 UI/session 使用，不进入 provider context。

### Grep / Glob

- [ ] Grep 的单行和最终字节截断保持 UTF-8 边界；当前直接按字节切片。
- [ ] Glob 在结果元数据中增加搜索根目录。
- [ ] 如果增加 `respect_gitignore` 等模式，必须显式参数化，不能隐式改变当前 `--no-ignore` 语义。

### Bash

- [ ] 清理 ANSI 转义和不可见控制字符，同时保留换行和制表符。
- [ ] 将 timeout、取消、退出码、截断统计和完整输出路径写入结构化 tool result；当前最终 shell result 主要依赖文本。

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

- [ ] apply_patch 先解析并验证所有文件和 hunk，再统一提交；当前跨文件 patch 中途失败可能留下前面已应用的修改。
- [ ] 文件工具结果增加机器可读状态，同时保留简短的模型可读摘要。
- [ ] 将只读工具和文件变更工具显式分类：只读操作可并行，同一路径的变更必须串行。
- [ ] 为取消、timeout、后台完成和长任务建立跨工具统一的结构化事件。

### 可选工具

- [ ] `Plan`：为长任务和 WebUI 提供结构化步骤及状态；优先级中。
- [ ] `RequestUserInput`：WebUI 需要结构化选项交互时引入；优先级中。
- [ ] `ViewImage`：现有 Read 已能读图，先完成缩放；优先级低。
- [ ] `ToolSearch`：工具数量显著增长后再按需加载；优先级低。
- [ ] `Agent` / 多代理：单 agent 生命周期稳定后再评估；优先级低。
- [ ] `Sleep` / `CurrentTime`：只有需要无副作用等待或取时才引入；优先级低。
