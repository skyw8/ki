# System Prompt

`internal/prompt.Build` 从 session 的资源快照和本轮运行输入渲染 system prompt，不直接读取磁盘或探测系统。

## 分层结构

System prompt 按以下顺序组装：

1. **身份与职责**：说明模型运行在 ki agent harness 中，可以读取文件、执行命令、修改代码和创建文件。
2. **Ki 配置位置**：存在 `KI_HOME` 时，列出 `ki.toml`、`.mcp.json`、`skills/`、`models.json`、`credentials.json`、项目级 `<cwd>/.ki/`，以及 `ki config path`。
3. **可用工具**：逐项输出本轮工具的名称和简短说明；没有工具时输出 `(none)`。这里包括内置工具以及已经绑定的 MCP 工具，并补充项目可能提供其他自定义工具。
4. **通用行为约束**：要求回答简洁，并在操作文件时清晰展示路径。
5. **追加系统指令**：读取项目级 `<cwd>/.ki/prompt/APPEND_SYSTEM.md`；不存在或不可读时读取全局 `{KI_HOME}/prompt/APPEND_SYSTEM.md`。项目文件覆盖全局文件，内容位于通用行为约束之后、Skills 之前，不替换 Ki 的基础 prompt。
6. **扩展追加**：启用的全局 extension `prompt.append` 文件，按扩展名序，每段 `<extension_instructions name="…">`。用户第 5 层仍覆盖全局 APPEND；扩展层在 Skills 之前。
7. **Skills**：仅当本轮存在 `Read` 工具且至少有一个启用的 skill 时输出。每个 skill 包含名称、描述和 `SKILL.md` 路径，同时说明按需读取及相对路径解析规则。
8. **项目指令**：输出 AGENTS/CLAUDE 文件的路径和完整内容。先加载 `{KI_HOME}` 下的全局文件，再按 git 仓库根目录到 cwd 的顺序加载；不在 git 仓库中时只加载 cwd。每个目录按 `AGENTS.override.md`、`AGENTS.md`、`AGENTS.MD`、`CLAUDE.md`、`CLAUDE.MD` 的优先级选取一个文件。
9. **运行系统**：输出 OS（macOS、Windows、Linux 或 WSL）和架构。
10. **当前环境**：输出 session cwd、资源快照创建日期和时区。

Prompt templates 不直接进入 system prompt，只用于 slash command 展开。`.mcp.json` 也不直接进入 system prompt；启用并成功绑定的 MCP tools 通过“可用工具”层体现。

## Reload

以下情况会执行全局 reload：

- `POST /v1/reload` 或 `/reload`。
- skills、MCP 或 extensions Toggle 修改成功。
- 自动或手动 compaction 成功。

Reload 会清空空闲 session 的资源快照并关闭 MCP 连接。正在跑 prompt 或 compact 的 session 把 reload 排到 `occupy` 对应的 `release` 之后。删除 session 时只清理该 session 的快照。

Compaction 会重建模型上下文，此时旧 prompt 缓存可视为失效；同步 reload 可以让下一轮使用最新的项目指令、skills、prompt templates、MCP 配置和运行环境。

## 缓存与动态计算

### 缓存

`resources.Loader` 以真实 session ID 为唯一缓存键。首次加载时生成完整 `Snapshot`，包括：

- OS、架构、KI_HOME、cwd、日期和时区。
- AGENTS/CLAUDE 文件。
- 项目级或全局级追加 system prompt。
- skills 元数据。
- prompt templates。
- 合并后的 `.mcp.json`。

同一 session 后续消息复用该快照，直到 reload。设置页没有 session，使用不缓存的 `Scan(cwd)`。

### 动态计算

每轮动态输入只有：

- 当前工具列表；模型能力或 MCP 状态可能改变它。
- Skills Toggle；决定 system prompt 展示哪些 skills。

MCP Toggle 不直接传入 `Build`，但会影响绑定到本轮的 MCP 工具。`Build` 本身每轮仍执行字符串组装，但不再读取资源或重新计算运行环境。
